// Package main runs console, the standalone sandbox operations UI of the
// open-ads stack. It is a dependency-free static file server that
// fingerprints the served files and embeds a revision watcher into index.html
// so any edit to web/ reloads open browser tabs automatically.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// revisionInterval defines how often the served console UI checks for changed
// files; tiny enough for instant feedback, coarse enough to stay invisible.
const revisionInterval = 1500 * time.Millisecond

// revisionScript is injected into the console index.html so that edits to the
// web/ files reload the browser automatically. Polling the revision endpoint
// avoids any websocket infrastructure; the endpoint exposes no data beyond an
// opaque fingerprint.
const revisionScript = `<script>
(function () {
  var known = null;
  function check() {
    fetch("/revision", { cache: "no-store", credentials: "omit" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (body) {
        if (!body || typeof body.revision !== "string") return;
        if (known === null) { known = body.revision; return; }
        if (body.revision !== known) {
          window.clearTimeout(timer);
          location.reload();
        }
      })
      .catch(function () { /* keep polling; the server may be restarting */ });
  }
  var timer = window.setInterval(check, %d);
})();
</script>`

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	webDir := os.Getenv("CONSOLE_WEB_DIR")
	if webDir == "" {
		webDir = "web"
	}
	port := os.Getenv("CONSOLE_PORT")
	if port == "" {
		port = "8083"
	}
	if _, err := os.Stat(filepath.Join(webDir, "index.html")); err != nil {
		logger.Error("console web directory is missing", slog.String("web_dir", webDir), slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("starting console server", slog.String("addr", ":"+port), slog.String("web_dir", webDir))
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           NewServer(webDir),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("console server fatal error", slog.Any("error", err))
		os.Exit(1)
	}
}

// NewServer returns the console HTTP handler rooted at the given web
// directory: static files, the /revision fingerprint endpoint and the
// live-reload watcher injected into index.html.
func NewServer(webDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok", "service": "console"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ready", "service": "console"})
	})
	mux.HandleFunc("GET /revision", func(w http.ResponseWriter, r *http.Request) {
		// CORS is open so the page may also be served by tooling elsewhere
		// while still watching this console's file fingerprint.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		revision, files, err := directoryRevision(webDir)
		if err != nil {
			writeJSON(w, map[string]any{"revision": "", "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"revision": revision, "files": files})
	})
	mux.Handle("/api/dsp/", newDSPAdminProxy())
	mux.Handle("/api/dmp/", newDMPAdminProxy())
	mux.Handle("/", &staticHandler{webDir: webDir})
	return mux
}

// dspAdminProxyTLSGrace is how long a proxied admin request is allowed to
// take; the sandbox console never batch-loads, so this is generous.
const dspAdminProxyTimeout = 15 * time.Second

// newDSPAdminProxy builds the browser-facing bridge to the DSP management
// API. The DSP admin contract is the ONLY upstream — no database, no
// Valkey, no side channels — and the bearer token stays server side so the
// UI cannot leak it. Without a configured token the endpoint answers 503
// instead of silently proxying unauthenticated requests.
func newDSPAdminProxy() http.Handler {
	logger := slog.Default()
	target := strings.TrimRight(os.Getenv("DSP_ADMIN_URL"), "/")
	if target == "" {
		target = "http://127.0.0.1:8081"
	}
	upstream, err := url.Parse(target)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		logger.Error("DSP_ADMIN_URL is not a valid admin upstream URL", slog.String("target", target), slog.Any("error", err))
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		logger.Warn("DSP admin upstream failed", slog.String("upstream", target), slog.Any("error", proxyErr))
		writeAPIError(w, http.StatusBadGateway, "dsp_admin_unreachable", "DSP 管理 API 不可达，请确认 DSP 服务在运行")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("Cache-Control", "no-store")
		return nil
	}
	token := ""
	if value := os.Getenv("DSP_ADMIN_TOKEN"); strings.TrimSpace(value) != "" {
		token = strings.TrimSpace(value)
	}
	transport := &http.Transport{
		// Bounded proxying: a sandbox console must not queue unbounded work.
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	proxy.Transport = transport

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "dsp_admin_unconfigured", "DSP 管理 API token 未配置（DSP_ADMIN_TOKEN）")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeAPIError(w, http.StatusMethodNotAllowed, "invalid_request", "console 仅转发 GET/POST/PUT 到 DSP 管理 API")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), dspAdminProxyTimeout)
		defer cancel()
		request := r.Clone(ctx)
		// /api/dsp/admin/v1/x → /admin/v1/x on the upstream.
		request.URL.Path = strings.TrimPrefix(request.URL.Path, "/api/dsp")
		if !strings.HasPrefix(request.URL.Path, "/admin/v1/") {
			writeAPIError(w, http.StatusNotFound, "not_found", "仅 /admin/v1/* 可通过代理访问")
			return
		}
		request.Host = upstream.Host
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Del("Cookie")
		proxy.ServeHTTP(w, request)
	})
}

// dmpAdminProxyPrefixes are the only DMP paths the console may reach. The
// management API serves definition CRUD; the internal paths are the operator
// actions (snapshot lifecycle, revocation, retention) that the admin token is
// allowed to drive in the sandbox.
var dmpAdminProxyPrefixes = []string{
	"/admin/v1/",
	"/internal/v1/snapshots",
	"/internal/v1/identity",
	"/internal/v1/retention",
	"/internal/v1/publications",
}

// newDMPAdminProxy bridges the browser to the DMP API. The token stays server
// side and the browser can only reach the allowlisted operator paths.
func newDMPAdminProxy() http.Handler {
	logger := slog.Default()
	target := strings.TrimRight(os.Getenv("DMP_ADMIN_URL"), "/")
	if target == "" {
		target = "http://127.0.0.1:8082"
	}
	upstream, err := url.Parse(target)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		logger.Error("DMP_ADMIN_URL is not a valid admin upstream URL", slog.String("target", target), slog.Any("error", err))
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		logger.Warn("DMP upstream failed", slog.String("upstream", target), slog.Any("error", proxyErr))
		writeAPIError(w, http.StatusBadGateway, "dmp_unreachable", "DMP 服务不可达，请确认 DMP 在运行")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("Cache-Control", "no-store")
		return nil
	}
	token := ""
	if value := os.Getenv("DMP_ADMIN_TOKEN"); strings.TrimSpace(value) != "" {
		token = strings.TrimSpace(value)
	}
	proxy.Transport = &http.Transport{
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "dmp_unconfigured", "DMP token 未配置（DMP_ADMIN_TOKEN）")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeAPIError(w, http.StatusMethodNotAllowed, "invalid_request", "console 仅转发 GET/POST/PUT 到 DMP")
			return
		}
		upstreamPath := strings.TrimPrefix(r.URL.Path, "/api/dmp")
		allowed := false
		for _, prefix := range dmpAdminProxyPrefixes {
			if strings.HasPrefix(upstreamPath, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeAPIError(w, http.StatusNotFound, "not_found", "该 DMP 路径不允许通过 console 访问")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), dspAdminProxyTimeout)
		defer cancel()
		request := r.Clone(ctx)
		request.URL.Path = upstreamPath
		request.Host = upstream.Host
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Del("Cookie")
		proxy.ServeHTTP(w, request)
	})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// staticHandler serves the web/ tree and injects the revision watcher into
// every served index.html variant.
type staticHandler struct {
	webDir string
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(filepath.ToSlash(r.URL.Path), "/")
	if rel == "" || strings.HasSuffix(rel, "/") {
		rel += "index.html"
	}
	if !strings.HasSuffix(rel, "index.html") {
		// The console is a sandbox: never let browsers pin stale scripts.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filepath.Join(h.webDir, filepath.FromSlash(rel)))
		return
	}
	raw, err := os.ReadFile(filepath.Join(h.webDir, filepath.FromSlash(rel)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	upper := strings.ToUpper(string(raw))
	pos := strings.LastIndex(upper, "</BODY>")
	if pos < 0 {
		http.ServeFile(w, r, filepath.Join(h.webDir, filepath.FromSlash(rel)))
		return
	}
	watcher := fmt.Sprintf(revisionScript, revisionInterval.Milliseconds())
	html := string(raw)[:pos] + watcher + string(raw)[pos:]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The served HTML differs from the file on disk; never cache it.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, html)
}

// directoryRevision fingerprints the web files by relative path, size and
// mtime, so edits, creations and deletions all move the revision.
func directoryRevision(basePath string) (string, int, error) {
	digest := sha256.New()
	files := 0
	err := filepath.WalkDir(basePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		rel, relErr := filepath.Rel(basePath, path)
		if relErr != nil {
			return relErr
		}
		files++
		fmt.Fprintf(digest, "%s|%d|%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", files, err
	}
	return hex.EncodeToString(digest.Sum(nil)), files, nil
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
