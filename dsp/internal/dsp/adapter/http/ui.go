package http

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// uiHandler serves the static prototype UI files located in the repository
// under docs/prototype. It strips the "/ui/" prefix from the request path and
// serves files from the resolved directory. If a directory is requested, it
// serves the index.html within that directory.
func uiHandler() http.Handler {
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
		// Normalise the request path: root or directory requests should serve
		// "index.html".
		if r.URL.Path == "" || r.URL.Path == "/" {
			r.URL.Path = "/index.html"
		} else if strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path += "index.html"
		}
		fs.ServeHTTP(w, r)
	})
}
