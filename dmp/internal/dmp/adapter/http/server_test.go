package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapterHTTP "github.com/songfei1983/open-ads/dmp/internal/dmp/adapter/http"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
)

func newTestServer() *adapterHTTP.Server {
	cfg := &config.Config{
		ServiceName: "test-dmp",
		Port:        8082,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return adapterHTTP.NewServer(cfg, logger)
}

func TestHealthz(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

func TestReadyz(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	srv.SetReady(false)
	respNotReady, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer respNotReady.Body.Close()

	if respNotReady.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", respNotReady.StatusCode)
	}
}

func TestAudienceResolveEndpoint(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"namespace":"publisher-account-v1","subject_token":"tok-123"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/internal/v1/audiences:resolve", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "018f7a3b2c9d7e1fa3b4c5d6e7f89070")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	var res map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if res["request_id"] != "018f7a3b2c9d7e1fa3b4c5d6e7f89070" {
		t.Errorf("expected request_id to be echoed, got %v", res["request_id"])
	}
}

func TestIdentityRevokeEndpoint(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"namespace":"publisher-account-v1"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/internal/v1/identity:revoke", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "018f7a3b2c9d7e1fa3b4c5d6e7f89071")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501 Not Implemented, got %d", resp.StatusCode)
	}
}
