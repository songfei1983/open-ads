package render_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/ledger"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/render"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

const tenantID = "00000000000000000000000000000001"

type fakeLedger struct {
	mu    sync.Mutex
	seen  map[string]string
	calls []ledger.Impression
	err   error
}

func (f *fakeLedger) RecordImpression(_ context.Context, imp ledger.Impression) (ledger.ImpressionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return ledger.ImpressionResult{}, f.err
	}
	if f.seen == nil {
		f.seen = map[string]string{}
	}
	f.calls = append(f.calls, imp)
	key := imp.AuctionID + "|" + imp.BidID + "|" + imp.ImpID
	if existing, ok := f.seen[key]; ok {
		return ledger.ImpressionResult{EventID: existing, Status: "duplicate"}, nil
	}
	f.seen[key] = "event-1"
	return ledger.ImpressionResult{EventID: "event-1", Status: "accepted"}, nil
}

func newCodec(t *testing.T) *token.Codec {
	t.Helper()
	codec, err := token.NewCodec([]byte(strings.Repeat("k", 32)), 1)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	return codec
}

func claims(t *testing.T, expires time.Time) token.EventClaims {
	return token.EventClaims{
		TenantID: tenantID, PublisherID: "018f7a3b2c9d7e1fa3b4c5d6e7f89001",
		PropertyID: "018f7a3b2c9d7e1fa3b4c5d6e7f89002", PlacementID: "018f7a3b2c9d7e1fa3b4c5d6e7f89005",
		AuctionID: "auction-1", BidID: "bid-1", ImpID: "1", CreativeID: "018f7a3b2c9d7e1fa3b4c5d6e7f89008",
		DSPEndpointID: "018f7a3b2c9d7e1fa3b4c5d6e7f89009", Reservation: "reservation-token",
		BidCPMMicros: 4_200_000, BillableMicros: 4200, Currency: "USD",
		BURLTarget: "https://dsp.example.com/rtb/v1/billable/bid-1", ExpiresAt: expires,
	}
}

func serve(t *testing.T, tracker *render.Tracker, audience, eventToken string) (*httptest.ResponseRecorder, map[string]string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"event_token": eventToken, "event_id": "client-event-1"})
	request := httptest.NewRequest(http.MethodPost, "/sdk/v1/impressions", strings.NewReader(string(body)))
	recorder := httptest.NewRecorder()
	tracker.Handle(recorder, request, audience)
	response := map[string]string{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return recorder, response
}

func TestTrackerBooksOncePerEventKey(t *testing.T) {
	codec := newCodec(t)
	store := &fakeLedger{}
	tracker := &render.Tracker{Codec: codec, TenantID: tenantID, Store: store}
	eventToken, err := codec.Seal(token.AudienceEvent, claims(t, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	first, body := serve(t, tracker, token.AudienceEvent, eventToken)
	if first.Code != http.StatusAccepted || body["status"] != "accepted" {
		t.Fatalf("first impression = %d %v, want 202 accepted", first.Code, body)
	}
	second, body := serve(t, tracker, token.AudienceEvent, eventToken)
	if second.Code != http.StatusOK || body["status"] != "duplicate" {
		t.Fatalf("replay = %d %v, want 200 duplicate", second.Code, body)
	}
	if store.calls[0].AmountMicros != domain.Micros(4200) {
		t.Fatalf("booked amount = %d, want 4200", store.calls[0].AmountMicros)
	}
	if store.calls[0].PublisherID == "" || store.calls[0].PropertyID == "" {
		t.Fatalf("impression is missing attribution: %+v", store.calls[0])
	}
}

func TestTrackerKeepsAudiencesSeparate(t *testing.T) {
	codec := newCodec(t)
	tracker := &render.Tracker{Codec: codec, TenantID: tenantID, Store: &fakeLedger{}}
	expiry := time.Now().Add(time.Hour)

	supplyToken, err := codec.Seal(token.AudienceSupplyEvent, claims(t, expiry))
	if err != nil {
		t.Fatalf("seal supply token: %v", err)
	}
	if recorder, _ := serve(t, tracker, token.AudienceEvent, supplyToken); recorder.Code != http.StatusBadRequest {
		t.Fatalf("supply token on the SDK audience = %d, want 400", recorder.Code)
	}
	if recorder, _ := serve(t, tracker, token.AudienceSupplyEvent, supplyToken); recorder.Code != http.StatusAccepted {
		t.Fatalf("supply token on the supply audience = %d, want 202", recorder.Code)
	}

	sdkToken, err := codec.Seal(token.AudienceEvent, claims(t, expiry))
	if err != nil {
		t.Fatalf("seal sdk token: %v", err)
	}
	if recorder, _ := serve(t, tracker, token.AudienceSupplyEvent, sdkToken); recorder.Code != http.StatusBadRequest {
		t.Fatalf("sdk token on the supply audience = %d, want 400", recorder.Code)
	}
}

func TestTrackerRejectsExpiredAndForeignTokens(t *testing.T) {
	codec := newCodec(t)
	tracker := &render.Tracker{Codec: codec, TenantID: tenantID, Store: &fakeLedger{}}

	expired, err := codec.Seal(token.AudienceEvent, claims(t, time.Now().Add(-time.Minute)))
	if err != nil {
		t.Fatalf("seal expired: %v", err)
	}
	if recorder, _ := serve(t, tracker, token.AudienceEvent, expired); recorder.Code != http.StatusBadRequest {
		t.Fatalf("expired token = %d, want 400", recorder.Code)
	}

	foreign := claims(t, time.Now().Add(time.Hour))
	foreign.TenantID = "00000000000000000000000000000002"
	foreignToken, err := codec.Seal(token.AudienceEvent, foreign)
	if err != nil {
		t.Fatalf("seal foreign: %v", err)
	}
	if recorder, _ := serve(t, tracker, token.AudienceEvent, foreignToken); recorder.Code != http.StatusBadRequest {
		t.Fatalf("foreign tenant token = %d, want 400", recorder.Code)
	}

	tampered := sdkTokenWithTamper(t, codec)
	if recorder, _ := serve(t, tracker, token.AudienceEvent, tampered); recorder.Code != http.StatusBadRequest {
		t.Fatalf("tampered token = %d, want 400", recorder.Code)
	}
}

func TestTrackerFailsClosedWithoutLedger(t *testing.T) {
	codec := newCodec(t)
	tracker := &render.Tracker{Codec: codec, TenantID: tenantID}
	eventToken, err := codec.Seal(token.AudienceEvent, claims(t, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if recorder, _ := serve(t, tracker, token.AudienceEvent, eventToken); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("impression without ledger = %d, want 503", recorder.Code)
	}

	failing := &fakeLedger{err: errors.New("database is down")}
	tracker.Store = failing
	if recorder, _ := serve(t, tracker, token.AudienceEvent, eventToken); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("impression with failing ledger = %d, want 503", recorder.Code)
	}
}

func sdkTokenWithTamper(t *testing.T, codec *token.Codec) string {
	t.Helper()
	value, err := codec.Seal(token.AudienceEvent, claims(t, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if value[len(value)-1] == 'A' {
		return value[:len(value)-1] + "B"
	}
	return value[:len(value)-1] + "A"
}
