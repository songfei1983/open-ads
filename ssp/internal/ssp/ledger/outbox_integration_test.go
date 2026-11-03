package ledger_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/ledger"
)

// TestConcurrentSendersDeliverEachEventOnce exercises the cross-instance claim:
// two senders polling the same outbox must never deliver the same event twice.
func TestConcurrentSendersDeliverEachEventOnce(t *testing.T) {
	store, _, _ := newStore(t)
	ctx := context.Background()

	var mu sync.Mutex
	delivered := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EventID string `json:"event_id"`
		}
		payload, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(payload, &body)
		mu.Lock()
		delivered[body.EventID]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	const events = 12
	for i := 0; i < events; i++ {
		imp := sampleImpression(t)
		imp.AuctionID = "concurrent-" + strconv.Itoa(i)
		imp.BidID = "bid-" + strconv.Itoa(i)
		imp.BURLTarget = server.URL + "/rtb/v1/billable/bid-" + strconv.Itoa(i)
		if _, err := store.RecordImpression(ctx, imp); err != nil {
			t.Fatalf("record impression %d: %v", i, err)
		}
	}

	newSender := func() *ledger.Sender {
		return &ledger.Sender{
			Store:  store,
			Client: server.Client(),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:    func() time.Time { return time.Now().UTC().Add(time.Hour) },
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender := newSender()
			for round := 0; round < 5; round++ {
				if _, err := sender.Drain(context.Background()); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("sender failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != events {
		t.Fatalf("delivered %d distinct events, want %d", len(delivered), events)
	}
	for eventID, count := range delivered {
		if count != 1 {
			t.Fatalf("event %s delivered %d times, want 1", eventID, count)
		}
	}
}
