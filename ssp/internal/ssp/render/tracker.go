package render

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

// Tracker accepts the sandbox impression signal from the render shell. It only
// deduplicates in process for iteration 5; durable, idempotent ledger writes
// arrive with the impression iteration.
type Tracker struct {
	Codec    *token.Codec
	TenantID string

	mu   sync.Mutex
	seen map[string]time.Time
}

type impressionRequest struct {
	EventToken string `json:"event_token"`
	EventID    string `json:"event_id"`
	OccurredAt string `json:"occurred_at"`
}

// Handle records one token-authenticated impression.
func (t *Tracker) Handle(w http.ResponseWriter, r *http.Request) {
	if t.Codec == nil {
		writeImpression(w, http.StatusServiceUnavailable, "rejected")
		return
	}
	var request impressionRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	if err := decoder.Decode(&request); err != nil || request.EventToken == "" {
		writeImpression(w, http.StatusBadRequest, "rejected")
		return
	}
	var claims token.EventClaims
	if err := t.Codec.Open(token.AudienceEvent, request.EventToken, &claims); err != nil {
		writeImpression(w, http.StatusBadRequest, "rejected")
		return
	}
	now := time.Now().UTC()
	if claims.TenantID != t.TenantID || claims.Expired(now) {
		writeImpression(w, http.StatusBadRequest, "rejected")
		return
	}
	key := claims.EventKey()
	t.mu.Lock()
	if t.seen == nil {
		t.seen = make(map[string]time.Time)
	}
	_, duplicate := t.seen[key]
	if !duplicate {
		t.seen[key] = now
	}
	t.pruneLocked(now)
	t.mu.Unlock()
	if duplicate {
		writeImpression(w, http.StatusOK, "duplicate")
		return
	}
	writeImpression(w, http.StatusAccepted, "accepted")
}

func (t *Tracker) pruneLocked(now time.Time) {
	if len(t.seen) < 1024 {
		return
	}
	for key, seenAt := range t.seen {
		if now.Sub(seenAt) > time.Hour {
			delete(t.seen, key)
		}
	}
}

func writeImpression(w http.ResponseWriter, status int, outcome string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": outcome})
}
