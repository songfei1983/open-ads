package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterHTTP "github.com/songfei1983/open-ads/ssp/internal/ssp/adapter/http"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/config"
)

func newTestServer() *adapterHTTP.Server {
	cfg := &config.Config{
		ServiceName: "test-ssp",
		Port:        8080,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := adapterHTTP.NewServer(cfg, logger)
	srv.SetReady(true)
	return srv
}

func TestAdRequestsOrchestratesDMPAndDSP(t *testing.T) {
	dmpCalled := false
	dmp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dmpCalled = true
		if r.URL.Path != "/internal/v1/audiences:resolve" {
			t.Fatalf("unexpected DMP path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"018f7a3b2c9d7e1fa3b4c5d6e7f89070","segments":[]}`))
	}))
	defer dmp.Close()

	dspCalled := false
	dsp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dspCalled = true
		if r.URL.Path != "/openrtb2/bid" {
			t.Fatalf("unexpected DSP path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"018f7a3b2c9d7e1fa3b4c5d6e7f89070","seatbid":[{"bid":[{"impid":"1","price":2.5,"adid":"ad-1","cid":"creative-1","adm":"<iframe src=\"https://render.test/asset\"></iframe>"}]}]}`))
	}))
	defer dsp.Close()

	cfg := &config.Config{
		ServiceName:    "test-ssp",
		Port:           8080,
		DMPURL:         dmp.URL,
		DSPURL:         dsp.URL,
		AuctionTimeout: 100 * time.Millisecond,
		DMPTimeout:     50 * time.Millisecond,
		DSPTimeout:     50 * time.Millisecond,
	}
	srv := adapterHTTP.NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/sdk/v1/ad-requests", strings.NewReader(`{"placement_id":"placement-1","slot":{"width":728,"height":90},"page":{"origin":"https://publisher.test","path":"/demo"},"consent":{"value":"subject-token"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "018f7a3b2c9d7e1fa3b4c5d6e7f89070")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Ad     struct {
			CreativeID string `json:"creative_id"`
			RenderURL  string `json:"render_url"`
		} `json:"ad"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !dmpCalled || !dspCalled || body.Status != "filled" || body.Ad.CreativeID != "creative-1" || body.Ad.RenderURL != "https://render.test/asset" {
		t.Fatalf("unexpected orchestration result: dmp=%v dsp=%v body=%+v", dmpCalled, dspCalled, body)
	}
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

	// 1. Ready state
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

	// 2. Not ready state
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

func TestAdRequestsEndpoint(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"placement_id":"test-placement","slot":{"id":"leaderboard","width":728,"height":90}}`)
	req, _ := http.NewRequest("POST", ts.URL+"/sdk/v1/ad-requests", body)
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

	if resp.Header.Get("X-Request-ID") != "018f7a3b2c9d7e1fa3b4c5d6e7f89070" {
		t.Errorf("expected X-Request-ID echo, got %s", resp.Header.Get("X-Request-ID"))
	}
}
