package lease

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

// SpendEntry is one advertiser spend ledger row.
type SpendEntry struct {
	EntryID      string `json:"entry_id"`
	SourceEvent  string `json:"source_event_id"`
	AdvertiserID string `json:"advertiser_id"`
	CampaignID   string `json:"campaign_id"`
	LineItemID   string `json:"line_item_id"`
	EntryType    string `json:"entry_type"`
	AmountMicros int64  `json:"amount_micros"`
	Reverses     string `json:"reverses_entry_id,omitempty"`
	Currency     string `json:"currency"`
	OccurredAt   string `json:"occurred_at"`
	RecordedAt   string `json:"recorded_at"`
}

// SpendAuditEntry is one immutable spend adjustment record.
type SpendAuditEntry struct {
	AuditID      string `json:"audit_id"`
	EntryID      string `json:"entry_id"`
	Reverses     string `json:"reverses_entry_id,omitempty"`
	Action       string `json:"action"`
	AmountMicros int64  `json:"amount_micros"`
	Reason       string `json:"reason"`
	Actor        string `json:"actor"`
	RequestID    string `json:"request_id,omitempty"`
	RecordedAt   string `json:"recorded_at"`
}

// ListSpendAudit returns the newest spend adjustment audit rows.
func (s *SQLBudgetStore) ListSpendAudit(ctx context.Context, limit int) ([]SpendAuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT audit_id, entry_id, reverses_entry_id, action,
		amount_micros, reason, actor, request_id, recorded_at
		FROM advertiser_spend_audit WHERE tenant_id = ? ORDER BY recorded_at DESC LIMIT ?`, s.tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list spend audit: %w", err)
	}
	defer rows.Close()

	entries := []SpendAuditEntry{}
	for rows.Next() {
		var auditID, entryID, reverses []byte
		var requestID sql.NullString
		var recordedAt time.Time
		var entry SpendAuditEntry
		if err := rows.Scan(&auditID, &entryID, &reverses, &entry.Action, &entry.AmountMicros,
			&entry.Reason, &entry.Actor, &requestID, &recordedAt); err != nil {
			return nil, fmt.Errorf("scan spend audit: %w", err)
		}
		entry.AuditID = hex.EncodeToString(auditID)
		entry.EntryID = hex.EncodeToString(entryID)
		if len(reverses) > 0 {
			entry.Reverses = hex.EncodeToString(reverses)
		}
		entry.RequestID = requestID.String
		entry.RecordedAt = recordedAt.UTC().Format(time.RFC3339Nano)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// ListSpendLedger returns the newest advertiser spend entries.
func (s *SQLBudgetStore) ListSpendLedger(ctx context.Context, limit int) ([]SpendEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entry_id, source_event_id, advertiser_id, campaign_id,
		line_item_id, entry_type, amount_micros, COALESCE(reverses_entry_id, ''), currency,
		occurred_at, recorded_at
		FROM advertiser_spend_ledger WHERE tenant_id = ? ORDER BY recorded_at DESC LIMIT ?`, s.tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list advertiser spend ledger: %w", err)
	}
	defer rows.Close()

	entries := []SpendEntry{}
	for rows.Next() {
		var entryID, sourceEvent, advertiser, campaign, lineItem, reverses []byte
		var occurredAt, recordedAt time.Time
		var entry SpendEntry
		if err := rows.Scan(&entryID, &sourceEvent, &advertiser, &campaign, &lineItem, &entry.EntryType,
			&entry.AmountMicros, &reverses, &entry.Currency, &occurredAt, &recordedAt); err != nil {
			return nil, fmt.Errorf("scan advertiser spend ledger: %w", err)
		}
		entry.EntryID = hex.EncodeToString(entryID)
		entry.SourceEvent = hex.EncodeToString(sourceEvent)
		entry.AdvertiserID = hex.EncodeToString(advertiser)
		entry.CampaignID = hex.EncodeToString(campaign)
		entry.LineItemID = hex.EncodeToString(lineItem)
		entry.OccurredAt = occurredAt.UTC().Format(time.RFC3339Nano)
		entry.RecordedAt = recordedAt.UTC().Format(time.RFC3339Nano)
		if len(reverses) > 0 {
			entry.Reverses = hex.EncodeToString(reverses)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// AppendSpendReversal appends a counter-entry against an original billable spend
// entry. Reversals are idempotent by content and capped by the original amount.
func (s *SQLBudgetStore) AppendSpendReversal(ctx context.Context, entryID string, amount domain.Micros, reason string) (string, error) {
	return s.AppendSpendReversalWithAudit(ctx, entryID, amount, reason, "", "")
}

// AppendSpendReversalWithAudit records the operator and request id alongside the
// reversal.
func (s *SQLBudgetStore) AppendSpendReversalWithAudit(ctx context.Context, entryID string, amount domain.Micros, reason, actor, requestID string) (string, error) {
	if amount <= 0 {
		return "", fmt.Errorf("%w: reversal amount must be positive", ErrInvalidSettlement)
	}
	originalBytes, err := normalizeUUIDBytes(entryID)
	if err != nil {
		return "", fmt.Errorf("%w: entry_id must be a 32-hex UUID", ErrInvalidSettlement)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", fmt.Errorf("begin spend reversal transaction: %w", err)
	}
	defer tx.Rollback()

	var entryType, currency string
	var original int64
	var occurredAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT entry_type, amount_micros, currency, occurred_at
		FROM advertiser_spend_ledger WHERE tenant_id = ? AND entry_id = ? FOR UPDATE`,
		s.tenantID, originalBytes).Scan(&entryType, &original, &currency, &occurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSpendEntryNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock spend entry: %w", err)
	}
	if entryType != "billable" {
		return "", fmt.Errorf("%w: only billable entries can be reversed", ErrInvalidSettlement)
	}

	var reversed int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(-amount_micros), 0) FROM advertiser_spend_ledger
		WHERE tenant_id = ? AND reverses_entry_id = ?`, s.tenantID, originalBytes).Scan(&reversed); err != nil {
		return "", fmt.Errorf("sum existing spend reversals: %w", err)
	}
	if reversed+int64(amount) > original {
		return "", fmt.Errorf("%w: cumulative reversal exceeds the original amount", ErrInvalidSettlement)
	}

	idempotency := spendReversalKey(originalBytes, int64(amount), reason)
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT entry_id FROM advertiser_spend_ledger
		WHERE tenant_id = ? AND idempotency_key = ?`, s.tenantID, idempotency[:]).Scan(&existing)
	if err == nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit replayed spend reversal: %w", commitErr)
		}
		return hex.EncodeToString(existing), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check replayed spend reversal: %w", err)
	}

	entryUUID, err := domain.NewUUIDv7()
	if err != nil {
		return "", fmt.Errorf("create spend reversal id: %w", err)
	}
	entryBytes, _ := hex.DecodeString(entryUUID)
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO advertiser_spend_ledger
		(tenant_id, entry_id, idempotency_key, source_event_id, advertiser_id, campaign_id, line_item_id,
		 entry_type, amount_micros, reverses_entry_id, currency, occurred_at, recorded_at)
		SELECT ?, ?, ?, source_event_id, advertiser_id, campaign_id, line_item_id,
		 'reversal', ?, ?, currency, ?, ?
		FROM advertiser_spend_ledger WHERE tenant_id = ? AND entry_id = ?`,
		s.tenantID, entryBytes, idempotency[:], -int64(amount), originalBytes, occurredAt, now,
		s.tenantID, originalBytes)
	if err != nil {
		return "", fmt.Errorf("insert spend reversal: %w", err)
	}
	auditID, err := domain.NewUUIDv7()
	if err != nil {
		return "", fmt.Errorf("create spend audit id: %w", err)
	}
	auditBytes, _ := hex.DecodeString(auditID)
	if strings.TrimSpace(actor) == "" {
		actor = "finance-operator"
	}
	var requestValue any
	if strings.TrimSpace(requestID) != "" {
		requestValue = requestID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO advertiser_spend_audit
		(tenant_id, audit_id, entry_id, reverses_entry_id, action, amount_micros, reason, actor, request_id, recorded_at)
		VALUES (?, ?, ?, ?, 'reversal', ?, ?, ?, ?, ?)`,
		s.tenantID, auditBytes, entryBytes, originalBytes, -int64(amount), reason, actor, requestValue, now); err != nil {
		return "", fmt.Errorf("insert spend reversal audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit spend reversal: %w", err)
	}
	return entryUUID, nil
}

// ErrSpendEntryNotFound is returned when the referenced spend entry is missing.
var ErrSpendEntryNotFound = errors.New("spend ledger entry not found")

func spendReversalKey(original []byte, amount int64, reason string) [32]byte {
	digest := sha256.New()
	digest.Write([]byte("openads.spend.reversal.v1"))
	digest.Write(original)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(amount))
	digest.Write(buf[:])
	digest.Write([]byte(reason))
	var key [32]byte
	copy(key[:], digest.Sum(nil))
	return key
}

// NewAdminHandler exposes the DSP finance surface: the advertiser spend ledger,
// its adjustment audit and append-only reversals. Any configured bearer secret
// is accepted; D-01 replaces this with role-scoped identities.
func NewAdminHandler(store *SQLBudgetStore, bearerSecrets ...string) http.Handler {
	secrets := make([]string, 0, len(bearerSecrets))
	for _, secret := range bearerSecrets {
		if trimmed := strings.TrimSpace(secret); trimmed != "" {
			secrets = append(secrets, trimmed)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if store == nil || len(secrets) == 0 {
			writeLedgerError(w, http.StatusServiceUnavailable, "dependency_unavailable", "ledger API is not configured")
			return
		}
		if !authorized(r, secrets) {
			writeLedgerError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/admin/v1/spend-ledger")
		switch {
		case path == "" && r.Method == http.MethodGet:
			listSpendLedger(w, r, store)
		case path == "/audit" && r.Method == http.MethodGet:
			listSpendAudit(w, r, store)
		case strings.HasPrefix(path, "/") && strings.HasSuffix(path, "/reversal") && r.Method == http.MethodPost:
			appendSpendReversal(w, r, store, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/reversal"))
		default:
			writeLedgerError(w, http.StatusNotFound, "not_found", "resource not found")
		}
	})
}

func listSpendLedger(w http.ResponseWriter, r *http.Request, store *SQLBudgetStore) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeLedgerError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	entries, err := store.ListSpendLedger(r.Context(), limit)
	if err != nil {
		writeLedgerError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to read the spend ledger")
		return
	}
	writeLedgerJSON(w, http.StatusOK, entries)
}

func listSpendAudit(w http.ResponseWriter, r *http.Request, store *SQLBudgetStore) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeLedgerError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	entries, err := store.ListSpendAudit(r.Context(), limit)
	if err != nil {
		writeLedgerError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to read the spend audit")
		return
	}
	writeLedgerJSON(w, http.StatusOK, entries)
}

func appendSpendReversal(w http.ResponseWriter, r *http.Request, store *SQLBudgetStore, entryID string) {
	var request struct {
		AmountMicros int64  `json:"amount_micros"`
		Reason       string `json:"reason"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 512 {
		writeLedgerError(w, http.StatusBadRequest, "invalid_request", "amount_micros and a reason of at most 512 characters are required")
		return
	}
	reversalID, err := store.AppendSpendReversalWithAudit(r.Context(), entryID,
		domain.Micros(request.AmountMicros), request.Reason,
		strings.TrimSpace(r.Header.Get("X-Actor")), w.Header().Get("X-Request-ID"))
	switch {
	case errors.Is(err, ErrSpendEntryNotFound):
		writeLedgerError(w, http.StatusNotFound, "not_found", "no spend entry with that id")
	case errors.Is(err, ErrInvalidSettlement):
		writeLedgerError(w, http.StatusUnprocessableEntity, "reversal_rejected", err.Error())
	case err != nil:
		writeLedgerError(w, http.StatusServiceUnavailable, "dependency_unavailable", "unable to append the reversal")
	default:
		writeLedgerJSON(w, http.StatusCreated, map[string]any{"entry_id": reversalID, "reverses_entry_id": entryID})
	}
}

func authorized(r *http.Request, secrets []string) bool {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return false
	}
	provided := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
	matched := 0
	for _, secret := range secrets {
		expected := sha256.Sum256([]byte(secret))
		matched |= subtle.ConstantTimeCompare(provided[:], expected[:])
	}
	return matched == 1
}

func writeLedgerError(w http.ResponseWriter, status int, code, message string) {
	payload := map[string]string{"code": code, "message": message}
	if requestID := w.Header().Get("X-Request-ID"); requestID != "" {
		payload["request_id"] = requestID
	}
	writeLedgerJSON(w, status, payload)
}

func writeLedgerJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
