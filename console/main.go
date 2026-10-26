// Package main runs console, the standalone sandbox operations UI of the
// open-ads stack. It is a dependency-free static file server that
// fingerprints the served files and embeds a revision watcher into index.html
// so any edit to web/ reloads open browser tabs automatically.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	mux.Handle("/", &staticHandler{webDir: webDir})
	return mux
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
