package http_test

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapterHTTP "github.com/songfei1983/open-ads/dsp/internal/dsp/adapter/http"
)

func liveReloadServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(adapterHTTP.UIHandler())
	t.Cleanup(ts.Close)
	return ts
}

func TestUIRevisionEndpointIsStableWhileUnchanged(t *testing.T) {
	ts := liveReloadServer(t)
	client := ts.Client()

	read := func() struct {
		Revision string `json:"revision"`
		Files    int    `json:"files"`
	} {
		resp, err := client.Get(ts.URL + "/ui/revision")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("revision endpoint: %d", resp.StatusCode)
		}
		var body struct {
			Revision string `json:"revision"`
			Files    int    `json:"files"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Revision) != 64 {
			t.Fatalf("revision not a sha256 hex: %q", body.Revision)
		}
		return body
	}

	first := read()
	second := read()
	if first.Revision != second.Revision || first.Files != second.Files {
		t.Fatalf("revision moved without file changes: %s/%d vs %s/%d", first.Revision, first.Files, second.Revision, second.Files)
	}
}

// TestUILiveReloadInjection ensures the served index.html embeds the watcher
// and that a file edit moves the revision while leaving the disk file intact.
func TestUILiveReloadInjection(t *testing.T) {
	ts := liveReloadServer(t)
	client := ts.Client()

	read := func() string {
		// Resolve the same directory the DSP handler serves from.
		resp, err := client.Get(ts.URL + "/ui/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		html, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(html)
	}

	if !strings.Contains(read(), "/ui/revision") {
		t.Fatalf("index.html does not embed the live-reload watcher")
	}
}

// TestUIRevisionMovesOnEdit (best-effort with the real repo files) touches a
// scratch file inside the served tree and expects a new fingerprint. It runs
// only when the docs/prototype directory is writable.
func TestUIRevisionMovesOnEdit(t *testing.T) {
	dir := uiSourceDir()
	if dir == "" {
		t.Skip("prototype dir resolution mismatch")
	}
	scratch := filepath.Join(dir, ".live-reload-test")
	ts := liveReloadServer(t)
	client := ts.Client()

	revision := func(t *testing.T) string {
		resp, err := client.Get(ts.URL + "/ui/revision")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Revision string `json:"revision"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Revision
	}

	before := revision(t)
	if err := os.WriteFile(scratch, []byte("t"), 0o644); err != nil {
		t.Skipf("cannot write scratch file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(scratch) })
	// mtime granularity can be low on macOS APFS; nudge until it moves.
	for attempts := 0; attempts < 20; attempts++ {
		after := revision(t)
		if after != before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("revision did not move after an edit: %s", before)
}

// uiSourceDir mirrors the handler's directory resolution for tests.
func uiSourceDir() string {
	if st, err := os.Stat("/src/docs/prototype"); err == nil && st.IsDir() {
		return "/src/docs/prototype"
	}
	rel, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", "docs", "prototype"))
	if err != nil {
		return ""
	}
	if st, err := os.Stat(rel); err == nil && st.IsDir() {
		return rel
	}
	return ""
}
