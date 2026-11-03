package ledger_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/ledger"
)

type recordedCall struct {
	eventID    string
	httpStatus int
	errText    string
	next       time.Time
}

type fakeOutbox struct {
	items      []ledger.OutboxItem
	delivered  []recordedCall
	retried    []recordedCall
	quarantine []recordedCall
}

func (f *fakeOutbox) ClaimDue(context.Context, time.Time, int, time.Duration) ([]ledger.OutboxItem, error) {
	items := f.items
	f.items = nil
	return items, nil
}

func (f *fakeOutbox) MarkDelivered(_ context.Context, eventID string, httpStatus int) error {
	f.delivered = append(f.delivered, recordedCall{eventID: eventID, httpStatus: httpStatus})
	return nil
}

func (f *fakeOutbox) MarkRetry(_ context.Context, eventID string, httpStatus int, errText string, next time.Time) error {
	f.retried = append(f.retried, recordedCall{eventID: eventID, httpStatus: httpStatus, errText: errText, next: next})
	return nil
}

func (f *fakeOutbox) MarkQuarantined(_ context.Context, eventID string, httpStatus int, errText string) error {
	f.quarantine = append(f.quarantine, recordedCall{eventID: eventID, httpStatus: httpStatus, errText: errText})
	return nil
}

func senderFixture(t *testing.T, handler http.HandlerFunc) (*ledger.Sender, *fakeOutbox) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store := &fakeOutbox{items: []ledger.OutboxItem{{
		EventID:        "0123456789abcdef0123456789abcdef",
		SourceEventKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DSPID:          "0123456789abcdef0123456789abcdef",
		AuctionID:      "auction-1",
		BidID:          "bid-1",
		ImpID:          "1",
		ReservationRef: "reservation-token",
		BURLTarget:     server.URL + "/rtb/v1/billable/bid-1",
		AmountMicros:   4200,
		Currency:       "USD",
		OccurredAt:     time.Now().UTC(),
	}}}
	sender := &ledger.Sender{
		Store:  store,
		Client: server.Client(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return sender, store
}

func TestSenderDeliversBurlPayload(t *testing.T) {
	var received map[string]any
	sender, store := senderFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	sent, err := sender.Drain(context.Background())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if sent != 1 || len(store.delivered) != 1 {
		t.Fatalf("sent=%d delivered=%d, want 1/1", sent, len(store.delivered))
	}
	for _, field := range []string{"event_id", "source_event_key", "auction_id", "bid_id", "imp_id", "reservation_ref", "currency"} {
		if value, _ := received[field].(string); value == "" {
			t.Errorf("payload is missing %s: %v", field, received)
		}
	}
	if amount, _ := received["amount_micros"].(float64); amount != 4200 {
		t.Errorf("payload amount = %v, want 4200", received["amount_micros"])
	}
}

func TestSenderRetriesTransientFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		sender, store := senderFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})
		if _, err := sender.Drain(context.Background()); err != nil {
			t.Fatalf("drain: %v", err)
		}
		if len(store.retried) != 1 || len(store.quarantine) != 0 || len(store.delivered) != 0 {
			t.Fatalf("status %d: retried=%d quarantine=%d delivered=%d, want 1/0/0",
				status, len(store.retried), len(store.quarantine), len(store.delivered))
		}
		if !store.retried[0].next.After(time.Now()) {
			t.Fatalf("status %d: next attempt %s is not in the future", status, store.retried[0].next)
		}
	}
}

func TestSenderQuarantinesPermanentRejections(t *testing.T) {
	sender, store := senderFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	if _, err := sender.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(store.quarantine) != 1 || len(store.retried) != 0 {
		t.Fatalf("quarantine=%d retried=%d, want 1/0", len(store.quarantine), len(store.retried))
	}
	if store.quarantine[0].errText != "http_400" {
		t.Fatalf("quarantine reason = %q, want http_400", store.quarantine[0].errText)
	}
}

func TestSenderQuarantinesWhenRetryBudgetIsExhausted(t *testing.T) {
	sender, store := senderFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	store.items[0].AttemptCount = 8
	if _, err := sender.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(store.quarantine) != 1 || store.quarantine[0].errText != "retry_budget_exhausted" {
		t.Fatalf("quarantine = %+v, want retry_budget_exhausted", store.quarantine)
	}
}

func TestSenderDoesNotFollowRedirects(t *testing.T) {
	var followed bool
	sender, store := senderFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rtb/v1/billable/bid-1" {
			http.Redirect(w, r, "/internal/secret", http.StatusFound)
			return
		}
		followed = true
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := sender.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if followed {
		t.Fatal("sender followed a redirect")
	}
	if len(store.quarantine) != 1 {
		t.Fatalf("quarantine=%d, want 1 (3xx is not a delivery)", len(store.quarantine))
	}
}
