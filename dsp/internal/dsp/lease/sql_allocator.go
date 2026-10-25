package lease

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

// Reserver defines the minimal interface required by a bidder decisioner
// to atomically hold and settle budget risk.
type Reserver interface {
	Reserve(id string, demands []Demand, now time.Time) (Reservation, error)
	Settle(id string, outcome Outcome, actual domain.Micros, now time.Time) (Reservation, error)
	Available(scopeID string, now time.Time) domain.Micros
}

// SettlementParams encapsulates the data needed to finalize a reservation in MariaDB.
type SettlementParams struct {
	TenantID       string
	ReservationID  string
	Outcome        Outcome // "loss" or "billable"
	ActualAmount   domain.Micros
	Currency       string
	SourceEventKey string
	Demands        []Demand
	LeaseIDs       map[string]string

	// Metadata for billable delivery events and advertiser spend ledger:
	EventID      string
	AuctionID    string
	BidID        string
	ImpID        string
	AdvertiserID string
	CampaignID   string
	LineItemID   string
	CreativeID   string
	OccurredAt   time.Time
}

// AccountRecord represents a row from the budget_account table.
type AccountRecord struct {
	TenantID          string
	LineItemID        string
	BudgetScope       string
	PeriodStart       time.Time
	Currency          string
	BudgetLimitMicros domain.Micros
	SpentMicros       domain.Micros
	LeasedMicros      domain.Micros
	Version           uint64
	UpdatedAt         time.Time
}

// LeaseRecord represents a row from the budget_lease table.
type LeaseRecord struct {
	TenantID             string
	LeaseID              string
	LineItemID           string
	BudgetScope          string
	PeriodStart          time.Time
	OwnerInstance        string
	AmountMicros         domain.Micros
	HeldMicros           domain.Micros
	SettlementGraceUntil time.Time
	Status               string
	ExpiresAt            time.Time
}

// SQLBudgetStore manages persistent budget accounts, leases, and settlements in MariaDB.
type SQLBudgetStore struct {
	db       *sql.DB
	tenantID []byte
}

// NewSQLBudgetStore creates a new MariaDB budget repository scoped to a tenant.
func NewSQLBudgetStore(db *sql.DB, tenantID string) (*SQLBudgetStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	tenantBytes, err := normalizeUUIDBytes(tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant id: %w", err)
	}
	return &SQLBudgetStore{db: db, tenantID: tenantBytes}, nil
}

// EnsureAccount creates or updates a budget account for a line item and scope.
// UTC period_start is truncated to day (YYYY-MM-DD) for daily scopes.
func (s *SQLBudgetStore) EnsureAccount(ctx context.Context, lineItemID, scope string, periodStart time.Time, currency string, limit domain.Micros) error {
	lineItemBytes, err := normalizeUUIDBytes(lineItemID)
	if err != nil {
		return fmt.Errorf("invalid line item ID: %w", err)
	}
	if limit < 0 {
		return domain.ErrNegativeAmount
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "daily"
	}
	normPeriod := normalizePeriodStart(scope, periodStart)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) != 3 {
		currency = "USD"
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO budget_account
		(tenant_id, line_item_id, budget_scope, period_start, currency, budget_limit_micros, spent_micros, leased_micros, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, 1, NOW(6))
		ON DUPLICATE KEY UPDATE budget_limit_micros = VALUES(budget_limit_micros), updated_at = NOW(6)`,
		s.tenantID, lineItemBytes, scope, normPeriod, currency, limit)
	if err != nil {
		return fmt.Errorf("ensure budget account: %w", err)
	}
	return nil
}

// AllocateLease atomically reserves an available budget slice from budget_account into a new budget_lease.
func (s *SQLBudgetStore) AllocateLease(ctx context.Context, lineItemID, scope string, periodStart time.Time, ownerInstance string, requested domain.Micros, duration time.Duration) (LeaseGrant, error) {
	if requested <= 0 {
		return LeaseGrant{}, ErrInvalidLease
	}
	lineItemBytes, err := normalizeUUIDBytes(lineItemID)
	if err != nil {
		return LeaseGrant{}, fmt.Errorf("invalid line item ID: %w", err)
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	normPeriod := normalizePeriodStart(scope, periodStart)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return LeaseGrant{}, fmt.Errorf("begin allocate lease tx: %w", err)
	}
	defer tx.Rollback()

	var limit, spent, leased int64
	var version uint64
	err = tx.QueryRowContext(ctx, `SELECT budget_limit_micros, spent_micros, leased_micros, version
		FROM budget_account WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ? FOR UPDATE`,
		s.tenantID, lineItemBytes, scope, normPeriod).Scan(&limit, &spent, &leased, &version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LeaseGrant{}, ErrInsufficientBudget
		}
		return LeaseGrant{}, fmt.Errorf("query budget account: %w", err)
	}

	available := limit - (spent + leased)
	if available <= 0 {
		return LeaseGrant{}, ErrInsufficientBudget
	}
	granted := int64(requested)
	if granted > available {
		granted = available
	}

	_, err = tx.ExecContext(ctx, `UPDATE budget_account
		SET leased_micros = leased_micros + ?, version = version + 1, updated_at = NOW(6)
		WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ?`,
		granted, s.tenantID, lineItemBytes, scope, normPeriod)
	if err != nil {
		return LeaseGrant{}, fmt.Errorf("update leased micros on budget account: %w", err)
	}

	leaseUUID, err := domain.NewUUIDv7()
	if err != nil {
		return LeaseGrant{}, fmt.Errorf("generate lease ID: %w", err)
	}
	leaseBytes, _ := hex.DecodeString(leaseUUID)
	now := time.Now().UTC()
	expiresAt := now.Add(duration)
	graceUntil := expiresAt.Add(300 * time.Second) // 300s settlement grace window

	_, err = tx.ExecContext(ctx, `INSERT INTO budget_lease
		(tenant_id, lease_id, line_item_id, budget_scope, period_start, owner_instance, amount_micros, held_micros, settlement_grace_until, status, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, 'active', ?)`,
		s.tenantID, leaseBytes, lineItemBytes, scope, normPeriod, ownerInstance, granted, graceUntil, expiresAt)
	if err != nil {
		return LeaseGrant{}, fmt.Errorf("insert budget lease: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return LeaseGrant{}, fmt.Errorf("commit allocate lease tx: %w", err)
	}

	scopeID := scope
	if lineItemID != "" {
		scopeID = lineItemID + ":" + scope
	}
	return LeaseGrant{
		ScopeID:   scopeID,
		LeaseID:   leaseUUID,
		Available: domain.Micros(granted),
		Held:      0,
		ExpiresAt: expiresAt,
	}, nil
}

// SettleReservation finalizes a reservation in MariaDB, updating lease held amounts,
// account spent/leased balances, recording settlements, delivery events, and ledgers.
func (s *SQLBudgetStore) SettleReservation(ctx context.Context, params SettlementParams) error {
	if params.ReservationID == "" || (params.Outcome != OutcomeLoss && params.Outcome != OutcomeBillable) {
		return ErrInvalidSettlement
	}
	if params.Outcome == OutcomeLoss && params.ActualAmount != 0 {
		return ErrInvalidSettlement
	}
	resBytes, err := normalizeUUIDBytes(params.ReservationID)
	if err != nil {
		return fmt.Errorf("invalid reservation ID: %w", err)
	}
	sourceKeyBytes := normalize32Bytes(params.SourceEventKey)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin settlement tx: %w", err)
	}
	defer tx.Rollback()

	// Check if already settled
	var existingOutcome string
	var existingActual int64
	err = tx.QueryRowContext(ctx, `SELECT outcome, actual_amount_micros
		FROM budget_reservation_settlement WHERE tenant_id = ? AND reservation_id = ? LIMIT 1 FOR UPDATE`,
		s.tenantID, resBytes).Scan(&existingOutcome, &existingActual)
	if err == nil {
		if existingOutcome == string(params.Outcome) && existingActual == int64(params.ActualAmount) {
			// Idempotent success
			return tx.Commit()
		}
		// Conflicting terminal outcome or amount
		qID, _ := domain.NewUUIDv7()
		qBytes, _ := hex.DecodeString(qID)
		payloadHash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", params.Outcome, params.ActualAmount)))
		_, _ = tx.ExecContext(ctx, `INSERT INTO dsp_notice_quarantine
			(tenant_id, quarantine_id, reservation_id, source_event_key, outcome, reason_code, payload_hash, status, received_at)
			VALUES (?, ?, ?, ?, ?, 'settlement_conflict', ?, 'pending', NOW(6))
			ON DUPLICATE KEY UPDATE reason_code = VALUES(reason_code)`,
			s.tenantID, qBytes, resBytes, sourceKeyBytes, string(params.Outcome), payloadHash[:])
		_ = tx.Commit()
		return fmt.Errorf("%w: conflicting outcome %q for reservation %q", ErrReservationConflict, params.Outcome, params.ReservationID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing reservation settlement: %w", err)
	}

	now := time.Now().UTC()
	settledAt := now
	currency := strings.ToUpper(strings.TrimSpace(params.Currency))
	if len(currency) != 3 {
		currency = "USD"
	}

	for _, demand := range params.Demands {
		if params.ActualAmount > demand.AmountMicros {
			return fmt.Errorf("%w: actual amount exceeds reserved risk", ErrInvalidSettlement)
		}
		leaseIDStr := params.LeaseIDs[demand.ScopeID]
		if leaseIDStr == "" {
			continue
		}
		leaseBytes, err := normalizeUUIDBytes(leaseIDStr)
		if err != nil {
			return fmt.Errorf("invalid lease ID: %w", err)
		}

		var lineItemBytes []byte
		var scope string
		var periodStart time.Time
		var held int64
		err = tx.QueryRowContext(ctx, `SELECT line_item_id, budget_scope, period_start, held_micros
			FROM budget_lease WHERE tenant_id = ? AND lease_id = ? FOR UPDATE`,
			s.tenantID, leaseBytes).Scan(&lineItemBytes, &scope, &periodStart, &held)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: lease not found for scope %q", ErrInvalidSettlement, demand.ScopeID)
			}
			return fmt.Errorf("query lease: %w", err)
		}

		// Deduct held amount from lease
		_, err = tx.ExecContext(ctx, `UPDATE budget_lease
			SET held_micros = GREATEST(0, held_micros - ?) WHERE tenant_id = ? AND lease_id = ?`,
			demand.AmountMicros, s.tenantID, leaseBytes)
		if err != nil {
			return fmt.Errorf("update lease held micros: %w", err)
		}

		// Deduct leased amount from account, and record spend if billable
		if params.Outcome == OutcomeBillable {
			_, err = tx.ExecContext(ctx, `UPDATE budget_account
				SET leased_micros = GREATEST(0, leased_micros - ?),
				    spent_micros = spent_micros + ?,
				    version = version + 1,
				    updated_at = NOW(6)
				WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ?`,
				demand.AmountMicros, params.ActualAmount, s.tenantID, lineItemBytes, scope, periodStart)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE budget_account
				SET leased_micros = GREATEST(0, leased_micros - ?),
				    version = version + 1,
				    updated_at = NOW(6)
				WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ?`,
				demand.AmountMicros, s.tenantID, lineItemBytes, scope, periodStart)
		}
		if err != nil {
			return fmt.Errorf("update budget account balance: %w", err)
		}

		// Record settlement row
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_reservation_settlement
			(tenant_id, reservation_id, budget_scope, period_start, lease_id, source_event_key, outcome, reserved_max_micros, actual_amount_micros, currency, settled_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.tenantID, resBytes, scope, periodStart, leaseBytes, sourceKeyBytes, string(params.Outcome),
			demand.AmountMicros, params.ActualAmount, currency, settledAt)
		if err != nil {
			return fmt.Errorf("insert budget reservation settlement: %w", err)
		}
	}

	// For billable outcomes, record dsp_delivery_event and advertiser_spend_ledger
	if params.Outcome == OutcomeBillable {
		eventUUID := params.EventID
		if eventUUID == "" {
			eventUUID, _ = domain.NewUUIDv7()
		}
		eventBytes, err := normalizeUUIDBytes(eventUUID)
		if err != nil {
			return fmt.Errorf("invalid event ID: %w", err)
		}
		advBytes := normalizeOptionalUUIDBytes(params.AdvertiserID)
		campBytes := normalizeOptionalUUIDBytes(params.CampaignID)
		liBytes := normalizeOptionalUUIDBytes(params.LineItemID)
		crBytes := normalizeOptionalUUIDBytes(params.CreativeID)
		occurredAt := params.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = now
		}

		_, err = tx.ExecContext(ctx, `INSERT INTO dsp_delivery_event
			(tenant_id, event_id, source_event_key, event_type, auction_id, bid_id, imp_id,
			 advertiser_id, campaign_id, line_item_id, creative_id, currency, amount_micros, occurred_at, received_at, schema_version)
			VALUES (?, ?, ?, 'billable', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW(6), 1)
			ON DUPLICATE KEY UPDATE received_at = VALUES(received_at)`,
			s.tenantID, eventBytes, sourceKeyBytes, params.AuctionID, params.BidID, params.ImpID,
			advBytes, campBytes, liBytes, crBytes, currency, params.ActualAmount, occurredAt)
		if err != nil {
			return fmt.Errorf("insert dsp delivery event: %w", err)
		}

		entryUUID, _ := domain.NewUUIDv7()
		entryBytes, _ := hex.DecodeString(entryUUID)
		idempKey := sha256.Sum256([]byte(params.EventID + ":" + params.SourceEventKey))
		_, err = tx.ExecContext(ctx, `INSERT INTO advertiser_spend_ledger
			(tenant_id, entry_id, idempotency_key, source_event_id, line_item_id, amount_micros)
			VALUES (?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE amount_micros = VALUES(amount_micros)`,
			s.tenantID, entryBytes, idempKey[:], eventBytes, liBytes, params.ActualAmount)
		if err != nil {
			return fmt.Errorf("insert advertiser spend ledger: %w", err)
		}
	}

	return tx.Commit()
}

// ReclaimExpiredLeases scans active leases past settlement_grace_until,
// returning any unheld leased budget back to budget_account, and marks leases expired.
func (s *SQLBudgetStore) ReclaimExpiredLeases(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin reclaim tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT lease_id, line_item_id, budget_scope, period_start, amount_micros, held_micros
		FROM budget_lease
		WHERE tenant_id = ? AND status = 'active' AND settlement_grace_until <= ? FOR UPDATE`,
		s.tenantID, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("query expired leases: %w", err)
	}
	defer rows.Close()

	type expiredItem struct {
		leaseID     []byte
		lineItemID  []byte
		scope       string
		periodStart time.Time
		amount      int64
		held        int64
	}
	var items []expiredItem
	for rows.Next() {
		var it expiredItem
		if err := rows.Scan(&it.leaseID, &it.lineItemID, &it.scope, &it.periodStart, &it.amount, &it.held); err != nil {
			return 0, fmt.Errorf("scan expired lease: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	reclaimed := 0
	for _, it := range items {
		unused := it.amount - it.held
		if unused > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE budget_account
				SET leased_micros = GREATEST(0, leased_micros - ?), version = version + 1, updated_at = NOW(6)
				WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ?`,
				unused, s.tenantID, it.lineItemID, it.scope, it.periodStart)
			if err != nil {
				return 0, fmt.Errorf("reclaim account leased balance: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE budget_lease SET status = 'expired'
			WHERE tenant_id = ? AND lease_id = ?`, s.tenantID, it.leaseID)
		if err != nil {
			return 0, fmt.Errorf("mark lease expired: %w", err)
		}
		reclaimed++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reclaim tx: %w", err)
	}
	return reclaimed, nil
}

func normalizePeriodStart(scope string, t time.Time) time.Time {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	if scope == "lifetime" {
		// Lifetime uses 1970-01-01 or specific start date
		return time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	// Daily truncates to midnight UTC
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func normalizeUUIDBytes(s string) ([]byte, error) {
	clean := strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if strings.HasPrefix(clean, "res") || strings.HasPrefix(clean, "bid") {
		clean = strings.TrimPrefix(clean, "res")
		clean = strings.TrimPrefix(clean, "bid")
	}
	if len(clean) == 32 {
		b, err := hex.DecodeString(clean)
		if err == nil {
			return b, nil
		}
	}
	// Fallback to deterministic hash for non-standard test IDs
	digest := sha256.Sum256([]byte(s))
	return digest[:16], nil
}

func normalizeOptionalUUIDBytes(s string) []byte {
	if s == "" {
		return make([]byte, 16)
	}
	b, err := normalizeUUIDBytes(s)
	if err != nil {
		digest := sha256.Sum256([]byte(s))
		return digest[:16]
	}
	return b
}

func normalize32Bytes(s string) []byte {
	clean := strings.ToLower(strings.TrimSpace(s))
	if len(clean) == 64 {
		b, err := hex.DecodeString(clean)
		if err == nil && len(b) == 32 {
			return b
		}
	}
	digest := sha256.Sum256([]byte(s))
	return digest[:]
}
