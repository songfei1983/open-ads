package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterHTTP "github.com/songfei1983/open-ads/dmp/internal/dmp/adapter/http"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
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

func TestInternalAPIIsDelegatedToTheInternalHandler(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Without a configured internal token or snapshot store the internal API
	// answers 503 instead of pretending to resolve audiences.
	resp, err := http.Post(ts.URL+"/internal/v1/audiences:resolve", "application/json",
		strings.NewReader(`{"namespace":"publisher-account-v1","subject_token":"`+strings.Repeat("ab", 32)+`"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured internal API should be 503, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "internal_api_unconfigured" {
		t.Fatalf("unexpected error code: %v", body)
	}
}

// slowSnapshotService proves internal API routes are not bound by the bid-path
// deadline, which is a few milliseconds and would cancel any ClickHouse read.
type slowSnapshotService struct {
	delay time.Duration
}

func (s slowSnapshotService) CreateJob(context.Context, snapshot.CreateJobInput) (snapshot.Job, error) {
	return snapshot.Job{}, errors.New("not used")
}

func (s slowSnapshotService) GetJob(context.Context, string) (snapshot.Job, error) {
	return snapshot.Job{}, errors.New("not used")
}

func (s slowSnapshotService) AppendRows(context.Context, string, []snapshot.MemberRow) (snapshot.AppendResult, error) {
	return snapshot.AppendResult{}, errors.New("not used")
}

func (s slowSnapshotService) Verify(context.Context, string) (snapshot.Job, error) {
	return snapshot.Job{}, errors.New("not used")
}

func (s slowSnapshotService) Publish(context.Context, string) (snapshot.Job, error) {
	return snapshot.Job{}, errors.New("not used")
}

func (s slowSnapshotService) Resolve(context.Context, snapshot.ResolveInput) (snapshot.ResolveResult, error) {
	return snapshot.ResolveResult{}, errors.New("not used")
}

func (s slowSnapshotService) ListJobs(ctx context.Context) ([]snapshot.Job, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []snapshot.Job{}, nil
}

func TestInternalAPIHasItsOwnBudget(t *testing.T) {
	cfg := &config.Config{
		ServiceName:    "test-dmp",
		Port:           8082,
		RequestTimeout: time.Millisecond,
		InternalToken:  "dmp-internal-token-0123456789abcdef",
		AdminActor:     "sandbox-operator",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := adapterHTTP.NewServer(cfg, logger, adapterHTTP.WithSnapshotService(slowSnapshotService{delay: 30 * time.Millisecond}))
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	request, err := http.NewRequest(http.MethodGet, ts.URL+"/internal/v1/snapshots", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+cfg.InternalToken)
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("internal API request should outlive the bid-path deadline, got %d", response.StatusCode)
	}
}
