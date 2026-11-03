package render

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/ledger"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

// Impressions records one authenticated impression. *ledger.Store implements it.
type Impressions interface {
	RecordImpression(ctx context.Context, imp ledger.Impression) (ledger.ImpressionResult, error)
}

// Tracker accepts token-authenticated impression signals and books them through
// the ledger. The browser SDK audience and the Supply audience share one ledger
// transaction, so a client-side and a server-side report for the same
// impression can never book twice.
type Tracker struct {
	Codec    *token.Codec
	TenantID string
	Store    Impressions
}

type impressionRequest struct {
	EventToken string `json:"event_token"`
	EventID    string `json:"event_id"`
	OccurredAt string `json:"occurred_at"`
}

// Handle records one token-authenticated impression for the given audience.
func (t *Tracker) Handle(w http.ResponseWriter, r *http.Request, audience string) {
	if t.Codec == nil || t.Store == nil {
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
	if err := t.Codec.Open(audience, request.EventToken, &claims); err != nil {
		writeImpression(w, http.StatusBadRequest, "rejected")
		return
	}
	now := time.Now().UTC()
	if claims.TenantID != t.TenantID || claims.Expired(now) {
		writeImpression(w, http.StatusBadRequest, "rejected")
		return
	}
	amount := claims.BillableMicros
	if amount <= 0 {
		computed, err := domain.CalculateBillableFromCPM(domain.Micros(claims.BidCPMMicros), 1)
		if err != nil || computed <= 0 {
			writeImpression(w, http.StatusBadRequest, "rejected")
			return
		}
		amount = int64(computed)
	}
	occurredAt := now
	if request.OccurredAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, request.OccurredAt); err == nil {
			occurredAt = parsed.UTC()
		} else if parsed, err := time.Parse(time.RFC3339, request.OccurredAt); err == nil {
			occurredAt = parsed.UTC()
		}
	}

	result, err := t.Store.RecordImpression(r.Context(), ledger.Impression{
		AuctionID:      claims.AuctionID,
		BidID:          claims.BidID,
		ImpID:          claims.ImpID,
		PublisherID:    claims.PublisherID,
		PropertyID:     claims.PropertyID,
		PlacementID:    claims.PlacementID,
		DSPID:          claims.DSPEndpointID,
		CreativeID:     claims.CreativeID,
		Currency:       claims.Currency,
		AmountMicros:   domain.Micros(amount),
		OccurredAt:     occurredAt,
		ReservationRef: claims.Reservation,
		BURLTarget:     claims.BURLTarget,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrEntryNotFound) {
			writeImpression(w, http.StatusBadRequest, "rejected")
			return
		}
		writeImpression(w, http.StatusServiceUnavailable, "rejected")
		return
	}
	status := http.StatusAccepted
	if result.Status == "duplicate" {
		status = http.StatusOK
	}
	writeImpression(w, status, result.Status)
}

func writeImpression(w http.ResponseWriter, status int, outcome string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": outcome})
}
