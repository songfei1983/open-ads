package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// uiRevisionInterval defines how often the served prototype UI checks for
// changed files; tiny enough for instant feedback, coarse enough to stay
// invisible in the request logs.
const uiRevisionInterval = 1500 * time.Millisecond

// uiRevisionScript is injected into the prototype index.html so that edits to
// the sandbox UI (HTML/CSS/JS) reload the browser automatically. Polling the
// revision endpoint avoids any websocket infrastructure; the endpoint exposes
// no data beyond an opaque fingerprint.
const uiRevisionScript = `<script>
(function () {
  var known = null;
  function check() {
    fetch("/ui/revision", { cache: "no-store", credentials: "omit" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (body) {
        if (!body || typeof body.revision !== "string") return;
        if (known === null) { known = body.revision; return; }
        if (body.revision !== known) {
          window.clearTimeout(timer);
          location.reload();
        }
      })
      .catch(function () { /* keep polling; server may be restarting (air) */ });
  }
  var timer = window.setInterval(check, %d);
})();
</script>`

// UIHandler serves the static prototype UI files located in the repository
// under docs/prototype. Directory requests resolve to index.html; the index
// additionally embeds a revision watcher so edits live-reload the page.
func UIHandler() http.Handler {
	// Determine the prototype UI directory.
	// In unit tests the working directory is the package directory, so we walk up
	// five levels to reach the repository root (as before). In the Docker image the
	// source is copied to /src, so we also check the absolute path "/src/docs/prototype".
	var basePath string
	// First, try the explicit container path.
	if _, err := os.Stat("/src/docs/prototype"); err == nil {
		basePath = "/src/docs/prototype"
	} else {
		// Fallback to the relative calculation used by tests.
		relPath, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", "docs", "prototype"))
		basePath = relPath
	}
	fs := http.FileServer(http.Dir(basePath))
	// The ServeMux pattern "/ui/" does **not** strip the prefix, so we need to
	// remove the leading "/ui" before handing the request to the file server.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Remove the "/ui" prefix.
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/ui")
		// Revision endpoint for the live-reload watcher. CORS is open so the
		// page may also be served locally (python http.server) while watching
		// the DSP-hosted prototype files.
		if r.URL.Path == "/revision" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			revision, files, err := directoryRevision(basePath)
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"revision": "", "error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"revision": revision, "files": files})
			return
		}
		// Normalise the request path: root or directory requests should serve
		// "index.html".
		if r.URL.Path == "" || r.URL.Path == "/" {
			r.URL.Path = "/index.html"
		} else if strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path += "index.html"
		}
		if strings.HasSuffix(r.URL.Path, "index.html") {
			servePrototyperIndex(w, r, basePath, fs)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// servePrototyperIndex serves the prototype index.html with the live-reload
// watcher injected before </body>.
func servePrototyperIndex(w http.ResponseWriter, r *http.Request, basePath string, fs http.Handler) {
	raw, err := os.ReadFile(filepath.Join(basePath, strings.TrimPrefix(filepath.ToSlash(r.URL.Path), "/")))
	if err != nil {
		fs.ServeHTTP(w, r)
		return
	}
	upper := strings.ToUpper(string(raw))
	pos := strings.LastIndex(upper, "</BODY>")
	if pos < 0 {
		fs.ServeHTTP(w, r)
		return
	}
	watcher := fmt.Sprintf(uiRevisionScript, uiRevisionInterval.Milliseconds())
	html := string(raw)[:pos] + watcher + string(raw)[pos:]
	head := http.CanonicalHeaderKey("Content-Type")
	if w.Header().Get(head) == "" {
		w.Header().Set(head, "text/html; charset=utf-8")
	}
	// The served HTML differs from the file on disk; render requests must
	// never be cached independently of the revision endpoint.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, html)
}

// directoryRevision fingerprints the prototype files by relative path, size
// and mtime, so edits and deletions both move the revision.
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
