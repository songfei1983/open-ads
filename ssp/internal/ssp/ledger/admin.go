package ledger

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
)

// NewAdminHandler exposes the finance-operator surface: the billable outbox and
// the append-only publisher payable ledger.
func NewAdminHandler(store *Store, bearerSecret string) http.Handler {
	h := &adminHandler{store: store, secret: bearerSecret}
	return http.HandlerFunc(h.serve)
}

type adminHandler struct {
	store  *Store
	secret string
}

func (h *adminHandler) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if h.store == nil || h.secret == "" {
		adminError(w, http.StatusServiceUnavailable, "dependency_unavailable", "ledger API is not configured")
		return
	}
	if !h.authorized(r) {
		adminError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/v1/")
	switch {
	case path == "billable-outbox" && r.Method == http.MethodGet:
		h.listOutbox(w, r)
	case strings.HasPrefix(path, "billable-outbox/") && r.Method == http.MethodPost:
		h.resolveOutbox(w, r, strings.TrimPrefix(path, "billable-outbox/"))
	case path == "publisher-payable" && r.Method == http.MethodGet:
		h.listPayable(w, r)
	case strings.HasPrefix(path, "publisher-payable/") && r.Method == http.MethodPost:
		h.reversal(w, r, strings.TrimPrefix(path, "publisher-payable/"))
	default:
		adminError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (h *adminHandler) authorized(r *http.Request) bool {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return false
	}
	provided := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
	expected := sha256.Sum256([]byte(h.secret))
	return subtle.ConstantTimeCompare(provided[:], expected[:]) == 1
}

func (h *adminHandler) listOutbox(w http.ResponseWriter, r *http.Request) {
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	switch status {
	case "", OutboxPending, OutboxSending, OutboxDelivered, OutboxQuarantined, OutboxRejected:
	default:
		adminError(w, http.StatusBadRequest, "invalid_request", "unknown outbox status")
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		adminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	items, err := h.store.ListOutbox(r.Context(), status, limit)
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to read the billable outbox")
		return
	}
	adminJSON(w, http.StatusOK, items)
}

func (h *adminHandler) resolveOutbox(w http.ResponseWriter, r *http.Request, action string) {
	eventID, ok := strings.CutSuffix(action, ":resolve")
	if !ok {
		adminError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	var request struct {
		Accept bool   `json:"accept"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &request); err != nil {
		adminError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 512 {
		adminError(w, http.StatusBadRequest, "invalid_request", "reason is required and must be 512 characters or fewer")
		return
	}
	if err := h.store.ResolveOutbox(r.Context(), eventID, request.Accept, request.Reason); err != nil {
		if errors.Is(err, ErrEntryNotFound) {
			adminError(w, http.StatusNotFound, "not_found", "no quarantined outbox row with that id")
			return
		}
		adminError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to resolve the billable outbox row")
		return
	}
	adminJSON(w, http.StatusOK, map[string]any{"event_id": eventID, "resolved": true})
}

func (h *adminHandler) listPayable(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		adminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	entries, err := h.store.ListPayable(r.Context(), limit)
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to read the publisher payable ledger")
		return
	}
	adminJSON(w, http.StatusOK, entries)
}

func (h *adminHandler) reversal(w http.ResponseWriter, r *http.Request, action string) {
	entryID, ok := strings.CutSuffix(action, ":reversal")
	if !ok {
		adminError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	var request struct {
		AmountMicros int64  `json:"amount_micros"`
		Reason       string `json:"reason"`
	}
	if err := decodeJSON(r, &request); err != nil {
		adminError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 512 {
		adminError(w, http.StatusBadRequest, "invalid_request", "reason is required and must be 512 characters or fewer")
		return
	}
	reversalID, err := h.store.AppendPayableReversal(r.Context(), Reversal{
		EntryID: entryID, AmountMicros: domain.Micros(request.AmountMicros), Reason: request.Reason,
	})
	switch {
	case errors.Is(err, ErrEntryNotFound):
		adminError(w, http.StatusNotFound, "not_found", "no payable entry with that id")
	case errors.Is(err, ErrReversalRejected):
		adminError(w, http.StatusUnprocessableEntity, "reversal_rejected", err.Error())
	case err != nil:
		adminError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to append the reversal")
	default:
		adminJSON(w, http.StatusCreated, map[string]any{"entry_id": reversalID, "reverses_entry_id": entryID})
	}
}

func parseLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > 200 {
		return 0, errors.New("limit must be between 1 and 200")
	}
	return limit, nil
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func adminError(w http.ResponseWriter, status int, code, message string) {
	payload := map[string]string{"code": code, "message": message}
	if requestID := w.Header().Get("X-Request-ID"); requestID != "" {
		payload["request_id"] = requestID
	}
	adminJSON(w, status, payload)
}

func adminJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
