package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
)

// ErrEntryNotFound is returned when a ledger or outbox row does not exist.
var ErrEntryNotFound = errors.New("ledger entry not found")

// ErrReversalRejected is returned when a reversal violates the append-only
// ledger rules (wrong type, wrong sign, wrong currency or over-reversal).
var ErrReversalRejected = errors.New("reversal rejected")

const mysqlDuplicateEntry = 1062

// Store persists SSP delivery events, publisher payables and the billable
// outbox. All financial writes are append-only.
type Store struct {
	db       *sql.DB
	tenant   []byte
	tenantID string
}

// NewStore creates a tenant-scoped ledger repository.
func NewStore(db *sql.DB, tenantID string) (*Store, error) {
	tenant, ok := encodeID(tenantID)
	if !ok {
		return nil, fmt.Errorf("tenant id %q is not a 32-hex UUID", tenantID)
	}
	return &Store{db: db, tenant: tenant, tenantID: normalizeID(tenantID)}, nil
}

// Impression is the authenticated event context recovered from a sealed token.
type Impression struct {
	AuctionID      string
	BidID          string
	ImpID          string
	PublisherID    string
	PropertyID     string
	PlacementID    string
	DSPID          string
	CreativeID     string
	Currency       string
	AmountMicros   domain.Micros
	OccurredAt     time.Time
	ReservationRef string
	BURLTarget     string
	PolicyVersion  uint64
	EventType      string
}

// ImpressionResult reports whether the impression was newly booked.
type ImpressionResult struct {
	EventID string
	Status  string // "accepted" for a new event, "duplicate" for a replay
}

// RecordImpression writes the delivery event, the publisher payable entry and
// the billable outbox row in one transaction. A replay of the same source event
// returns the original event id and books nothing.
func (s *Store) RecordImpression(ctx context.Context, imp Impression) (ImpressionResult, error) {
	eventType := imp.EventType
	if eventType == "" {
		eventType = EventTypeImpression
	}
	occurredAt := imp.OccurredAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	if imp.AmountMicros <= 0 {
		return ImpressionResult{}, fmt.Errorf("impression amount must be positive")
	}
	if imp.Currency == "" || len(imp.Currency) != 3 {
		return ImpressionResult{}, fmt.Errorf("impression currency must be a 3-letter code")
	}
	publisher, okPublisher := encodeID(imp.PublisherID)
	property, okProperty := encodeID(imp.PropertyID)
	placement, okPlacement := encodeID(imp.PlacementID)
	if !okPublisher || !okProperty || !okPlacement {
		return ImpressionResult{}, fmt.Errorf("impression identifiers are incomplete")
	}
	dsp, _ := encodeID(imp.DSPID)
	creative, _ := encodeID(imp.CreativeID)

	sourceKey := SourceEventKey(s.tenantID, imp.AuctionID, imp.BidID, imp.ImpID, eventType, 1)
	eventID, err := domain.NewUUIDv7()
	if err != nil {
		return ImpressionResult{}, fmt.Errorf("create delivery event id: %w", err)
	}
	eventBytes, _ := hex.DecodeString(eventID)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ImpressionResult{}, fmt.Errorf("begin impression transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `INSERT INTO ssp_delivery_event
		(tenant_id, event_id, source_event_key, event_type, auction_id, bid_id, imp_id,
		 publisher_id, property_id, placement_id, dsp_id, creative_id, currency, amount_micros,
		 occurred_at, received_at, schema_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		s.tenant, eventBytes, sourceKey[:], eventType, imp.AuctionID, imp.BidID, imp.ImpID,
		publisher, property, placement, nullBytes(dsp), nullBytes(creative), imp.Currency,
		int64(imp.AmountMicros), occurredAt, time.Now().UTC())
	if err != nil {
		if isDuplicate(err) {
			_ = tx.Rollback()
			var existing []byte
			if queryErr := s.db.QueryRowContext(ctx, `SELECT event_id FROM ssp_delivery_event
				WHERE tenant_id = ? AND source_event_key = ?`, s.tenant, sourceKey[:]).Scan(&existing); queryErr != nil {
				return ImpressionResult{}, fmt.Errorf("read duplicate delivery event: %w", queryErr)
			}
			return ImpressionResult{EventID: hex.EncodeToString(existing), Status: "duplicate"}, nil
		}
		return ImpressionResult{}, fmt.Errorf("insert delivery event: %w", err)
	}

	idempotency := PayableIdempotencyKey(sourceKey)
	policyVersion := imp.PolicyVersion
	if policyVersion == 0 {
		policyVersion = 1
	}
	entryID, err := domain.NewUUIDv7()
	if err != nil {
		return ImpressionResult{}, fmt.Errorf("create payable entry id: %w", err)
	}
	entryBytes, _ := hex.DecodeString(entryID)
	_, err = tx.ExecContext(ctx, `INSERT INTO publisher_payable_ledger
		(tenant_id, entry_id, idempotency_key, source_event_id, publisher_id, auction_id, imp_id,
		 entry_type, amount_micros, reverses_entry_id, currency, publisher_policy_version,
		 occurred_at, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'billable', ?, NULL, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE entry_id = entry_id`,
		s.tenant, entryBytes, idempotency[:], eventBytes, publisher, imp.AuctionID, imp.ImpID,
		int64(imp.AmountMicros), imp.Currency, policyVersion, occurredAt, time.Now().UTC())
	if err != nil {
		return ImpressionResult{}, fmt.Errorf("insert publisher payable: %w", err)
	}

	if imp.BURLTarget != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO ssp_billable_outbox
			(tenant_id, event_id, source_event_key, dsp_id, auction_id, bid_id, imp_id, reservation_ref,
			 burl_target_url, amount_micros, currency, occurred_at, attempt_count, next_attempt_at, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'pending')
			ON DUPLICATE KEY UPDATE event_id = event_id`,
			s.tenant, eventBytes, sourceKey[:], dsp, imp.AuctionID, imp.BidID, imp.ImpID,
			[]byte(imp.ReservationRef), imp.BURLTarget, int64(imp.AmountMicros), imp.Currency,
			occurredAt, time.Now().UTC())
		if err != nil {
			return ImpressionResult{}, fmt.Errorf("insert billable outbox: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ImpressionResult{}, fmt.Errorf("commit impression transaction: %w", err)
	}
	return ImpressionResult{EventID: eventID, Status: "accepted"}, nil
}

// PayableEntry is the read projection of one publisher ledger row.
type PayableEntry struct {
	EntryID       string    `json:"entry_id"`
	SourceEventID string    `json:"source_event_id"`
	PublisherID   string    `json:"publisher_id"`
	AuctionID     string    `json:"auction_id"`
	ImpID         string    `json:"imp_id"`
	EntryType     string    `json:"entry_type"`
	AmountMicros  int64     `json:"amount_micros"`
	Reverses      string    `json:"reverses_entry_id,omitempty"`
	Currency      string    `json:"currency"`
	OccurredAt    time.Time `json:"occurred_at"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// ListPayable returns the newest publisher ledger entries.
func (s *Store) ListPayable(ctx context.Context, limit int) ([]PayableEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entry_id, source_event_id, publisher_id, auction_id, imp_id,
		entry_type, amount_micros, COALESCE(reverses_entry_id, ''), currency, occurred_at, recorded_at
		FROM publisher_payable_ledger WHERE tenant_id = ? ORDER BY recorded_at DESC LIMIT ?`, s.tenant, limit)
	if err != nil {
		return nil, fmt.Errorf("list publisher payable: %w", err)
	}
	defer rows.Close()

	entries := []PayableEntry{}
	for rows.Next() {
		var entryID, sourceEvent, publisher, reverses []byte
		var entry PayableEntry
		if err := rows.Scan(&entryID, &sourceEvent, &publisher, &entry.AuctionID, &entry.ImpID,
			&entry.EntryType, &entry.AmountMicros, &reverses, &entry.Currency,
			&entry.OccurredAt, &entry.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan publisher payable: %w", err)
		}
		entry.EntryID = hex.EncodeToString(entryID)
		entry.SourceEventID = hex.EncodeToString(sourceEvent)
		entry.PublisherID = hex.EncodeToString(publisher)
		if len(reverses) > 0 {
			entry.Reverses = hex.EncodeToString(reverses)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// Reversal appends a counter-entry against one original billable entry.
type Reversal struct {
	EntryID      string
	AmountMicros domain.Micros // positive magnitude; stored negated
	Reason       string
}

// AppendPayableReversal appends a reversal entry. Partial reversals are allowed
// while the cumulative reversed magnitude stays within the original amount.
func (s *Store) AppendPayableReversal(ctx context.Context, reversal Reversal) (string, error) {
	if reversal.AmountMicros <= 0 {
		return "", fmt.Errorf("%w: reversal amount must be positive", ErrReversalRejected)
	}
	originalBytes, ok := encodeID(reversal.EntryID)
	if !ok {
		return "", fmt.Errorf("%w: entry_id must be a 32-hex UUID", ErrReversalRejected)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", fmt.Errorf("begin reversal transaction: %w", err)
	}
	defer tx.Rollback()

	var entryType, currency string
	var amount int64
	var publisher, auctionID, impID []byte
	var policyVersion uint64
	var occurredAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT entry_type, amount_micros, currency, publisher_id, auction_id, imp_id,
		publisher_policy_version, occurred_at
		FROM publisher_payable_ledger WHERE tenant_id = ? AND entry_id = ? FOR UPDATE`,
		s.tenant, originalBytes).Scan(&entryType, &amount, &currency, &publisher, &auctionID, &impID,
		&policyVersion, &occurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrEntryNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock original payable entry: %w", err)
	}
	if entryType != "billable" {
		return "", fmt.Errorf("%w: only billable entries can be reversed", ErrReversalRejected)
	}

	var reversed int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(-amount_micros), 0) FROM publisher_payable_ledger
		WHERE tenant_id = ? AND reverses_entry_id = ?`, s.tenant, originalBytes).Scan(&reversed); err != nil {
		return "", fmt.Errorf("sum existing reversals: %w", err)
	}
	if reversed+int64(reversal.AmountMicros) > amount {
		return "", fmt.Errorf("%w: cumulative reversal exceeds the original amount", ErrReversalRejected)
	}

	idempotency := reversalKey(originalBytes, int64(reversal.AmountMicros), reversal.Reason)
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT entry_id FROM publisher_payable_ledger
		WHERE tenant_id = ? AND idempotency_key = ?`, s.tenant, idempotency[:]).Scan(&existing)
	if err == nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit replayed reversal: %w", commitErr)
		}
		return hex.EncodeToString(existing), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check replayed reversal: %w", err)
	}

	entryID, err := domain.NewUUIDv7()
	if err != nil {
		return "", fmt.Errorf("create reversal entry id: %w", err)
	}
	entryBytes, _ := hex.DecodeString(entryID)
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO publisher_payable_ledger
		(tenant_id, entry_id, idempotency_key, source_event_id, publisher_id, auction_id, imp_id,
		 entry_type, amount_micros, reverses_entry_id, currency, publisher_policy_version,
		 occurred_at, recorded_at)
		SELECT ?, ?, ?, source_event_id, publisher_id, auction_id, imp_id,
		 'reversal', ?, ?, currency, ?, ?, ?
		FROM publisher_payable_ledger WHERE tenant_id = ? AND entry_id = ?`,
		s.tenant, entryBytes, idempotency[:], -int64(reversal.AmountMicros), originalBytes,
		policyVersion, occurredAt, now, s.tenant, originalBytes)
	if err != nil {
		return "", fmt.Errorf("insert payable reversal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit payable reversal: %w", err)
	}
	return entryID, nil
}

func isDuplicate(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// reversalKey makes a reversal idempotent by content: repeating the same
// original/amount/reason triple returns the first entry instead of appending.
func reversalKey(original []byte, amount int64, reason string) [32]byte {
	digest := sha256.New()
	digest.Write([]byte("openads.payable.reversal.v1"))
	digest.Write(original)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(amount))
	digest.Write(buf[:])
	digest.Write([]byte(reason))
	var key [32]byte
	copy(key[:], digest.Sum(nil))
	return key
}
