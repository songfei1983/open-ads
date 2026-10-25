package http

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUIHandler verifies that the uiHandler correctly serves static files from
// the docs/prototype directory. It checks that a known HTML file returns status
// 200 and contains the expected title element.
func TestUIHandler(t *testing.T) {
	handler := uiHandler()
	server := httptest.NewServer(handler)
	defer server.Close()

	// Request the main DSP prototype page.
	resp, err := http.Get(server.URL + "/ui/dsp.html")
	if err != nil {
		t.Fatalf("failed to GET ui/dsp.html: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body error: %v", err)
	}
	if !strings.Contains(string(body), "<title>DSP 控制台") {
		t.Fatalf("response does not contain expected title element")
	}
}

func TestUIHandlerConsoleJS(t *testing.T) {
	handler := uiHandler()
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/ui/console.js")
	if err != nil {
		t.Fatalf("GET console.js error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for console.js, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// Ensure the script defines the expected serviceKey variable.
	if !strings.Contains(string(body), "const serviceKey") {
		t.Fatalf("console.js does not contain expected serviceKey definition")
	}
}

func TestUIHandlerConsoleCSS(t *testing.T) {
	handler := uiHandler()
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/ui/console.css")
	if err != nil {
		t.Fatalf("GET console.css error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for console.css, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "body") {
		t.Fatalf("console.css does not contain expected CSS content")
	}
}
