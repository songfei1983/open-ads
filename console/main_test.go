package main_test

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	console "github.com/songfei1983/open-ads/console"
)

// webDir resolves the console/web directory regardless of the test working
// directory: the package tests run with cwd equal to the console folder, but
// tooling may start elsewhere in the module tree.
func webDir(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join("web", "index.html")); err == nil {
		return "web"
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		dir = filepath.Dir(dir)
		if _, err := os.Stat(filepath.Join(dir, "console", "web", "index.html")); err == nil {
			return filepath.Join(dir, "console", "web")
		}
	}
	t.Skip("console/web not resolvable from this working directory")
	return ""
}

func newConsoleServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := webDir(t)
	ts := httptest.NewServer(console.NewServer(dir))
	t.Cleanup(ts.Close)
	return ts
}

func TestRevisionStableWhileUnchanged(t *testing.T) {
	ts := newConsoleServer(t)
	client := ts.Client()

	read := func() string {
		resp, err := client.Get(ts.URL + "/revision")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("revision endpoint: %d", resp.StatusCode)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	first := read()
	second := read()
	if first != second {
		t.Fatalf("revision moved without changes:\n%s\n%s", first, second)
	}
	if !strings.Contains(first, `"files":`) {
		t.Fatalf("revision payload missing the file count: %s", first)
	}
}

func TestIndexEmbedsWatcher(t *testing.T) {
	ts := newConsoleServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	html, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `fetch("/revision"`) {
		t.Fatalf("index.html does not embed the revision watcher")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("index content type wrong: %s", ct)
	}
}

func TestRevisionMovesOnEdit(t *testing.T) {
	dir := webDir(t)
	ts := newConsoleServer(t)
	client := ts.Client()

	revision := func() string {
		resp, err := client.Get(ts.URL + "/revision")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	before := revision()
	scratch := filepath.Join(dir, "scratch.tmp")
	if err := os.WriteFile(scratch, []byte("x"), 0o644); err != nil {
		t.Skipf("cannot write scratch file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(scratch) })
	// mtime granularity can be low on macOS; nudge until it moves.
	for attempts := 0; attempts < 20; attempts++ {
		if revision() != before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("revision did not move after an edit: %s", before)
}
