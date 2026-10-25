package http

import (
	"net/http"
	"path/filepath"
	"strings"
)

// uiHandler serves the static prototype UI files located in the repository
// under docs/prototype. It strips the "/ui/" prefix from the request path and
// serves files from the resolved directory. If a directory is requested, it
// serves the index.html within that directory.
func uiHandler() http.Handler {
	// Resolve the absolute path to the prototype directory relative to the
	// repository root (the current working directory when tests are run). The
	// prototype assets live under docs/prototype.
	// The test binary runs with the current working directory set to the package
	// directory (dsp/internal/dsp/adapter/http). To locate the prototype assets
	// we need to walk up five directories to reach the repository root, then
	// descend into docs/prototype.
	basePath, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", "docs", "prototype"))
	fs := http.FileServer(http.Dir(basePath))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ensure the path starts with "/ui/"; otherwise fallback to NotFound.
		if !strings.HasPrefix(r.URL.Path, "/ui/") {
			http.NotFound(w, r)
			return
		}
		// Trim the "/ui" prefix so the file server can locate files correctly.
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/ui")
		// If the request points to a directory, serve index.html.
		if strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path += "index.html"
		}
		fs.ServeHTTP(w, r)
	})
}
