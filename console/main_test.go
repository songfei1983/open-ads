package main_test

import (
	"io"
	"net/http"
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

// newDSPAdminStub answers the admin requests the console is allowed to make
// and records what it saw, so the proxy contract can be asserted exactly.
func newDSPAdminStub(t *testing.T, recorded *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*recorded = append(*recorded, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		if r.URL.Path == "/admin/v1/error" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"code":"business_rule_violation","message":"invalid"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

func proxyClient(t *testing.T, env map[string]string) (*http.Client, string) {
	t.Helper()
	dir := webDir(t)
	for key, value := range env {
		t.Setenv(key, value)
	}
	// NewServer reads the env once, so build a fresh server per client.
	ts := httptest.NewServer(console.NewServer(dir))
	t.Cleanup(ts.Close)
	client := ts.Client()
	return client, ts.URL
}

func TestDSPAdminProxyInjectsTokenAndRewritesPath(t *testing.T) {
	var recorded []string
	stub := newDSPAdminStub(t, &recorded)
	client, baseURL := proxyClient(t, map[string]string{
		"DSP_ADMIN_URL":   stub.URL,
		"DSP_ADMIN_TOKEN": "secret-token",
	})

	resp, err := client.Get(baseURL + "/api/dsp/admin/v1/campaigns")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("proxied admin call: %d", resp.StatusCode)
	}
	if len(recorded) != 1 || recorded[0] != "GET /admin/v1/campaigns auth=Bearer secret-token" {
		t.Fatalf("unexpected upstream call: %v", recorded)
	}
}

func TestDSPAdminProxyRejectsNonAdminPathsAndMethods(t *testing.T) {
	var recorded []string
	stub := newDSPAdminStub(t, &recorded)
	client, baseURL := proxyClient(t, map[string]string{
		"DSP_ADMIN_URL":   stub.URL,
		"DSP_ADMIN_TOKEN": "secret-token",
	})

	resp, err := client.Get(baseURL + "/api/dsp/openrtb2/bid")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("non-admin path should be 404, got %d", resp.StatusCode)
	}

	request, err := http.NewRequest(http.MethodDelete, baseURL+"/api/dsp/admin/v1/campaigns/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteResp, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	deleteResp.Body.Close()
	if deleteResp.StatusCode != 405 {
		t.Fatalf("DELETE should be 405, got %d", deleteResp.StatusCode)
	}
	if len(recorded) != 0 {
		t.Fatalf("requests leaked to upstream: %v", recorded)
	}
}

func TestDSPAdminProxyRequiresToken(t *testing.T) {
	client, baseURL := proxyClient(t, map[string]string{"DSP_ADMIN_TOKEN": ""})
	resp, err := client.Get(baseURL + "/api/dsp/admin/v1/campaigns")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("missing token should be 503, got %d", resp.StatusCode)
	}
}

// newDMPStub answers the DMP paths the console may reach.
func newDMPStub(t *testing.T, recorded *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*recorded = append(*recorded, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`[]`))
	}))
}

func TestDMPProxyAllowsOnlyOperatorPaths(t *testing.T) {
	var recorded []string
	stub := newDMPStub(t, &recorded)
	for key, value := range map[string]string{
		"DMP_ADMIN_URL":   stub.URL,
		"DMP_ADMIN_TOKEN": "dmp-admin-token-0123456789abcdef",
	} {
		t.Setenv(key, value)
	}
	dir := webDir(t)
	ts := httptest.NewServer(console.NewServer(dir))
	t.Cleanup(ts.Close)
	client := ts.Client()

	allowed := []string{
		"/api/dmp/admin/v1/providers",
		"/api/dmp/internal/v1/snapshots",
		"/api/dmp/internal/v1/identity-revocations",
		"/api/dmp/internal/v1/retention:policy",
	}
	for _, path := range allowed {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s should be proxied, got %d", path, resp.StatusCode)
		}
	}
	if len(recorded) != len(allowed) {
		t.Fatalf("unexpected upstream calls: %v", recorded)
	}

	// The console must never reach resolve, health probes or unknown paths.
	for _, path := range []string{"/api/dmp/internal/v1/audiences:resolve", "/api/dmp/readyz", "/api/dmp/admin/v2/unknown"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s should be blocked, got %d", path, resp.StatusCode)
		}
	}
	if len(recorded) != len(allowed) {
		t.Fatalf("blocked paths leaked upstream: %v", recorded)
	}

	request, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/dmp/admin/v1/providers/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE should be 405, got %d", response.StatusCode)
	}
}

func TestDMPProxyRequiresToken(t *testing.T) {
	t.Setenv("DMP_ADMIN_TOKEN", "")
	dir := webDir(t)
	ts := httptest.NewServer(console.NewServer(dir))
	t.Cleanup(ts.Close)
	resp, err := ts.Client().Get(ts.URL + "/api/dmp/admin/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("missing DMP token should be 503, got %d", resp.StatusCode)
	}
}
