package http_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterHTTP "github.com/songfei1983/open-ads/dmp/internal/dmp/adapter/http"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
)

func newTestServer() *adapterHTTP.Server {
	cfg := &config.Config{
		ServiceName: "test-dmp",
		Port:        8082,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := adapterHTTP.NewServer(cfg, logger)
	srv.SetReady(true)
	return srv
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

// slowListStore proves the admin route is not bound by the bid-path deadline,
// which defaults to a few milliseconds and would cancel any ClickHouse write.
type slowListStore struct {
	control.Store
	delay time.Duration
}

func (s slowListStore) ListNamespaces(ctx context.Context) ([]control.Namespace, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []control.Namespace{}, nil
}

func TestAdminSurfaceIsWiredWithItsOwnBudget(t *testing.T) {
	cfg := &config.Config{
		ServiceName:    "test-dmp",
		Port:           8082,
		RequestTimeout: time.Millisecond,
		AdminToken:     "dmp-admin-token-0123456789abcdef",
		AdminTenantID:  "00000000000000000000000000000001",
		AdminActor:     "sandbox-operator",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := adapterHTTP.NewServer(cfg, logger, adapterHTTP.WithControlStore(slowListStore{delay: 30 * time.Millisecond}))
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	unauthorized, err := http.Get(ts.URL + "/admin/v1/namespaces")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d", unauthorized.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin request should outlive the bid-path deadline, got %d", response.StatusCode)
	}
}

func TestAdminSurfaceDisabledWithoutToken(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	request, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer any-token-any-token-any-token-1")
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured admin surface should be 503, got %d", response.StatusCode)
	}
}
