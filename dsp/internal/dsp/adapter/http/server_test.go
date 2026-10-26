package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapterHTTP "github.com/songfei1983/open-ads/dsp/internal/dsp/adapter/http"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/config"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/decision"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/lease"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

func newTestServer() *adapterHTTP.Server {
	cfg := &config.Config{
		ServiceName: "test-dsp",
		Port:        8081,
		BidTimeout:  50 * time.Millisecond,
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
	if err := snapshot.Register(&snapshot.Manifest{Version: "readyz-test-v1"}); err != nil {
		t.Fatalf("register snapshot: %v", err)
	}
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
	var readyBody map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode ready response: %v", err)
	}
	if got := readyBody["snapshot_version"]; got != "readyz-test-v1" {
		t.Errorf("expected active snapshot version, got %q", got)
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

func TestOpenRTBBidEndpoint(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"id":"req-018f7a3b","imp":[{"id":"1"}]}`)
	req, _ := http.NewRequest("POST", ts.URL+"/openrtb2/bid", body)
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
	if res["id"] != "req-018f7a3b" {
		t.Errorf("expected valid request id, got %v", res["id"])
	}
}

func TestBillableNoticeEndpoint(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"event_id":"018f7a3b","amount_micros":2500}`)
	req, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/billable/bid-123", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", resp.StatusCode)
	}
}

func TestWinNoticeEndpoint(t *testing.T) {
	srv := newTestServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/rtb/v1/win/bid-123?price=2.50", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
}

func TestOpenRTBBidReturnsValidBidWithReservation(t *testing.T) {
	const version = "server-openrtb-bid-v1"
	manifest := &snapshot.Manifest{
		Version: version,
		Campaigns: map[string]domain.Campaign{
			"camp-1": {ID: "camp-1", Active: true, LineItemIDs: []string{"li-1"}},
		},
		LineItems: map[string]domain.LineItem{
			"li-1": {
				ID: "li-1", CampaignID: "camp-1", Active: true,
				CreativeIDs: []string{"cr-1"}, SiteDomain: "example.com",
				SitePage: "https://example.com/sports", BannerWidth: 728, BannerHeight: 90,
				BidCPMMicros: 2_500_000, Currency: "USD",
			},
		},
		Creatives: map[string]domain.Creative{
			"cr-1": {
				ID: "cr-1", Active: true,
				AssetMeta: domain.AssetMeta{MIMEType: "image/png", SizeKB: 50, Hash: "sha256:123", Host: "cdn.example.com"},
			},
		},
	}
	if err := snapshot.Register(manifest); err != nil {
		t.Fatalf("register manifest: %v", err)
	}

	allocator := lease.NewBudgetAllocator()
	now := time.Now().UTC()
	if err := allocator.InstallLease("li-1:daily", "lease-1", 10_000, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	codec, err := lease.NewTokenCodec("v1", key)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTestServer()
	decisioner := decision.FixtureDecisioner{
		ManifestVersion: version,
		Whitelist:       []domain.WhiteListEntry{{Host: "cdn.example.com", MIME: "image/png"}},
		Reserver:        allocator,
		TokenCodec:      codec,
		BaseNoticeURL:   "http://127.0.0.1:8081",
		TenantID:        "018f7a3b2c9d7e1fa3b4c5d6e7f89010",
	}
	srv.SetDecisioner(decisioner)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{
		"id": "auction-001",
		"imp": [{"id": "imp-1", "banner": {"w": 728, "h": 90}}],
		"site": {"domain": "example.com", "page": "https://example.com/sports"},
		"cur": ["USD"]
	}`)
	req, _ := http.NewRequest("POST", ts.URL+"/openrtb2/bid", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var res struct {
		ID      string `json:"id"`
		SeatBid []struct {
			Bid []struct {
				ID    string  `json:"id"`
				ImpID string  `json:"impid"`
				Price float64 `json:"price"`
				AdID  string  `json:"adid"`
				NURL  string  `json:"nurl"`
				BURL  string  `json:"burl"`
				LURL  string  `json:"lurl"`
				CID   string  `json:"cid"`
				CRID  string  `json:"crid"`
				W     int     `json:"w"`
				H     int     `json:"h"`
				Ext   struct {
					ReservationID  string `json:"reservation_id"`
					ReservationRef string `json:"reservation_ref"`
				} `json:"ext"`
			} `json:"bid"`
		} `json:"seatbid"`
		BidID string `json:"bidid"`
		Cur   string `json:"cur"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode bid response: %v", err)
	}

	if res.ID != "auction-001" || len(res.SeatBid) == 0 || len(res.SeatBid[0].Bid) == 0 {
		t.Fatalf("invalid bid response structure: %+v", res)
	}
	bid := res.SeatBid[0].Bid[0]
	if bid.Price != 2.50 || bid.ImpID != "imp-1" || bid.CID != "camp-1" || bid.CRID != "cr-1" {
		t.Fatalf("bid fields mismatch: %+v", bid)
	}
	if bid.Ext.ReservationID == "" || bid.Ext.ReservationRef == "" {
		t.Fatalf("missing reservation in bid ext: %+v", bid.Ext)
	}
	if !strings.Contains(bid.BURL, "/rtb/v1/billable/") || !strings.Contains(bid.LURL, "/rtb/v1/loss/") {
		t.Fatalf("invalid notice URLs: nurl=%q, burl=%q, lurl=%q", bid.NURL, bid.BURL, bid.LURL)
	}
}

func TestBillableNoticeWithAEADTokenAndSettlement(t *testing.T) {
	key := make([]byte, 32)
	codec, err := lease.NewTokenCodec("v1", key)
	if err != nil {
		t.Fatal(err)
	}
	allocator := lease.NewBudgetAllocator()
	now := time.Now().UTC()
	if err := allocator.InstallLease("daily", "lease-1", 10_000, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	reservation, err := allocator.Reserve("res-001", []lease.Demand{{ScopeID: "daily", AmountMicros: 2500}}, now)
	if err != nil {
		t.Fatal(err)
	}

	claims := lease.ReservationClaims{
		TenantID:       "018f7a3b2c9d7e1fa3b4c5d6e7f89010",
		ReservationID:  reservation.ID,
		AuctionID:      "auc-001",
		BidID:          "bid-001",
		ImpressionID:   "1",
		Currency:       "USD",
		ReservedMicros: 2500,
		ExpiresAt:      now.Add(10 * time.Minute),
		Demands:        reservation.Demands,
		LeaseIDs:       reservation.LeaseIDs,
	}
	token, err := codec.Seal(claims)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTestServer()
	srv.SetReserver(allocator)
	srv.SetTokenCodec(codec)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Valid billable settlement -> 204 No Content
	billableBody := fmt.Sprintf(`{
		"event_id": "evt-001",
		"source_event_key": "src-001",
		"auction_id": "auc-001",
		"bid_id": "bid-001",
		"imp_id": "1",
		"reservation_ref": "%s",
		"amount_micros": 2500,
		"currency": "USD"
	}`, token)

	req, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/billable/bid-001", strings.NewReader(billableBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content, got %d", resp.StatusCode)
	}

	// 2. Replay with identical payload -> idempotent 204 No Content
	reqReplay, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/billable/bid-001", strings.NewReader(billableBody))
	reqReplay.Header.Set("Content-Type", "application/json")
	respReplay, err := http.DefaultClient.Do(reqReplay)
	if err != nil {
		t.Fatal(err)
	}
	defer respReplay.Body.Close()
	if respReplay.StatusCode != http.StatusNoContent {
		t.Fatalf("expected idempotent 204, got %d", respReplay.StatusCode)
	}

	// 3. Replay with conflicting amount -> 409 Conflict
	conflictBody := fmt.Sprintf(`{
		"event_id": "evt-002",
		"reservation_ref": "%s",
		"amount_micros": 1000,
		"currency": "USD"
	}`, token)
	reqConflict, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/billable/bid-001", strings.NewReader(conflictBody))
	reqConflict.Header.Set("Content-Type", "application/json")
	respConflict, err := http.DefaultClient.Do(reqConflict)
	if err != nil {
		t.Fatal(err)
	}
	defer respConflict.Body.Close()
	if respConflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d", respConflict.StatusCode)
	}

	// 4. Overdrawn amount (> reserved) -> 400 Bad Request
	overBody := fmt.Sprintf(`{
		"event_id": "evt-003",
		"reservation_ref": "%s",
		"amount_micros": 3000,
		"currency": "USD"
	}`, token)
	reqOver, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/billable/bid-001", strings.NewReader(overBody))
	reqOver.Header.Set("Content-Type", "application/json")
	respOver, err := http.DefaultClient.Do(reqOver)
	if err != nil {
		t.Fatal(err)
	}
	defer respOver.Body.Close()
	if respOver.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on overdrawn amount, got %d", respOver.StatusCode)
	}
}

func TestLossNoticeAndConflictingBillable(t *testing.T) {
	key := make([]byte, 32)
	codec, err := lease.NewTokenCodec("v1", key)
	if err != nil {
		t.Fatal(err)
	}
	allocator := lease.NewBudgetAllocator()
	now := time.Now().UTC()
	if err := allocator.InstallLease("daily", "lease-1", 10_000, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	reservation, err := allocator.Reserve("res-loss-001", []lease.Demand{{ScopeID: "daily", AmountMicros: 2000}}, now)
	if err != nil {
		t.Fatal(err)
	}

	claims := lease.ReservationClaims{
		TenantID:       "018f7a3b2c9d7e1fa3b4c5d6e7f89010",
		ReservationID:  reservation.ID,
		AuctionID:      "auc-loss-001",
		BidID:          "bid-loss-001",
		ImpressionID:   "1",
		Currency:       "USD",
		ReservedMicros: 2000,
		ExpiresAt:      now.Add(10 * time.Minute),
		Demands:        reservation.Demands,
		LeaseIDs:       reservation.LeaseIDs,
	}
	token, err := codec.Seal(claims)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTestServer()
	srv.SetReserver(allocator)
	srv.SetTokenCodec(codec)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Loss notice via POST -> 200 OK
	lossReq, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/loss/bid-loss-001?reason=102&ref="+token, nil)
	lossResp, err := http.DefaultClient.Do(lossReq)
	if err != nil {
		t.Fatal(err)
	}
	defer lossResp.Body.Close()
	if lossResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for loss notice, got %d", lossResp.StatusCode)
	}

	// 2. Subsequent billable notice for the same reservation must conflict (409)
	billableBody := fmt.Sprintf(`{
		"event_id": "evt-loss-001",
		"reservation_ref": "%s",
		"amount_micros": 2000,
		"currency": "USD"
	}`, token)
	reqBillable, _ := http.NewRequest("POST", ts.URL+"/rtb/v1/billable/bid-loss-001", strings.NewReader(billableBody))
	reqBillable.Header.Set("Content-Type", "application/json")
	respBillable, err := http.DefaultClient.Do(reqBillable)
	if err != nil {
		t.Fatal(err)
	}
	defer respBillable.Body.Close()
	if respBillable.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when settling billable after loss, got %d", respBillable.StatusCode)
	}
}

type timeoutDecisioner struct{}

func (timeoutDecisioner) Decide(ctx context.Context, req decision.Request) (decision.Response, error) {
	select {
	case <-ctx.Done():
		return decision.Response{RequestID: req.ID, NoBid: true, NoBidReason: 2}, ctx.Err()
	case <-time.After(500 * time.Millisecond):
		return decision.Response{RequestID: req.ID}, nil
	}
}

func TestOpenRTBBidDecisionTimeout(t *testing.T) {
	cfg := &config.Config{
		ServiceName: "test-dsp",
		Port:        8081,
		BidTimeout:  20 * time.Millisecond,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := adapterHTTP.NewServer(cfg, logger)
	srv.SetReady(true)
	srv.SetDecisioner(timeoutDecisioner{})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{
		"id": "timeout-req-1",
		"imp": [{"id": "imp-1", "banner": {"w": 300, "h": 250}}]
	}`)
	req, _ := http.NewRequest("POST", ts.URL+"/openrtb2/bid", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var res map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["nbr"] != float64(2) {
		t.Fatalf("expected nbr 2 on timeout, got %v", res["nbr"])
	}

	metricsRec := httptest.NewRecorder()
	srv.Metrics().Handler(metricsRec, httptest.NewRequest("GET", "/metrics", nil))
	metricsText := metricsRec.Body.String()
	if !strings.Contains(metricsText, "open_ads_dsp_decision_timeouts_total 1") {
		t.Fatalf("expected metrics to record 1 timeout, got:\n%s", metricsText)
	}
}
