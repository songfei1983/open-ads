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

	var settledLineItem []byte
	for _, demand := range params.Demands {
		if params.ActualAmount > demand.AmountMicros {
			return fmt.Errorf("%w: actual amount exceeds reserved risk", ErrInvalidSettlement)
		}
		leaseIDStr := params.LeaseIDs[demand.ScopeID]
		if leaseIDStr == "" {
			// Every applicable scope must settle together: leaving one behind
			// would leak a held reservation and overspend the line item.
			return fmt.Errorf("%w: missing lease for scope %q", ErrInvalidSettlement, demand.ScopeID)
		}
		leaseBytes, err := normalizeUUIDBytes(leaseIDStr)
		if err != nil {
			return fmt.Errorf("invalid lease ID: %w", err)
		}

		var lineItemBytes []byte
		var scope string
		var periodStart time.Time
		var held int64
		var graceUntil time.Time
		var materialized bool
		err = tx.QueryRowContext(ctx, `SELECT line_item_id, budget_scope, period_start, held_micros,
			settlement_grace_until
			FROM budget_lease WHERE tenant_id = ? AND lease_id = ? FOR UPDATE`,
			s.tenantID, leaseBytes).Scan(&lineItemBytes, &scope, &periodStart, &held, &graceUntil)
		if errors.Is(err, sql.ErrNoRows) {
			// Runtime budget holds live in Valkey; the settleable reservation is
			// bound to a scope id, not to a MariaDB lease row. Materialise the
			// lease so the settlement keeps its foreign key and audit trail.
			lineItemBytes, scope, periodStart, leaseBytes, err = s.materializeLease(ctx, tx, params, demand, now)
			if err != nil {
				return err
			}
			materialized = true
			graceUntil = now
		} else if err != nil {
			return fmt.Errorf("query lease: %w", err)
		}
		if settledLineItem == nil {
			settledLineItem = lineItemBytes
		}
		if !materialized && now.After(graceUntil) {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return fmt.Errorf("rollback expired settlement: %w", err)
			}
			s.quarantineLateNotice(ctx, params, "grace_expired")
			return fmt.Errorf("%w: scope %q grace ended at %s", ErrSettlementExpired,
				demand.ScopeID, graceUntil.UTC().Format(time.RFC3339))
		}

		// Deduct held amount from lease
		_, err = tx.ExecContext(ctx, `UPDATE budget_lease
			SET held_micros = GREATEST(0, held_micros - ?), status = 'settled'
			WHERE tenant_id = ? AND lease_id = ?`,
			demand.AmountMicros, s.tenantID, leaseBytes)
		if err != nil {
			return fmt.Errorf("update lease held micros: %w", err)
		}

		// The account is the money authority; a hold smaller than the settled
		// risk means the Valkey lease and MariaDB drifted. The settlement still
		// proceeds (money must be recorded) but the drift is parked for a
		// finance operator instead of being hidden by GREATEST(0, ...).
		var accountLeased int64
		if err := tx.QueryRowContext(ctx, `SELECT leased_micros FROM budget_account
			WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ? FOR UPDATE`,
			s.tenantID, lineItemBytes, scope, periodStart).Scan(&accountLeased); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: budget account missing for scope %q", ErrInvalidSettlement, demand.ScopeID)
			}
			return fmt.Errorf("query budget account: %w", err)
		}
		if accountLeased < int64(demand.AmountMicros) {
			if err := s.recordUnderflowQuarantine(ctx, tx, params, demand.ScopeID, accountLeased); err != nil {
				return err
			}
		}

		// Deduct leased amount from account, and record spend if billable
		var accountResult sql.Result
		if params.Outcome == OutcomeBillable {
			accountResult, err = tx.ExecContext(ctx, `UPDATE budget_account
				SET leased_micros = GREATEST(0, leased_micros - ?),
				    spent_micros = spent_micros + ?,
				    version = version + 1,
				    updated_at = NOW(6)
				WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ?`,
				demand.AmountMicros, params.ActualAmount, s.tenantID, lineItemBytes, scope, periodStart)
		} else {
			accountResult, err = tx.ExecContext(ctx, `UPDATE budget_account
				SET leased_micros = GREATEST(0, leased_micros - ?),
				    version = version + 1,
				    updated_at = NOW(6)
				WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = ? AND period_start = ?`,
				demand.AmountMicros, s.tenantID, lineItemBytes, scope, periodStart)
		}
		if err != nil {
			return fmt.Errorf("update budget account balance: %w", err)
		}
		if accountResult != nil {
			affected, rowsErr := accountResult.RowsAffected()
			if rowsErr == nil && affected == 0 {
				return fmt.Errorf("%w: budget account missing for scope %q", ErrInvalidSettlement, demand.ScopeID)
			}
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

	// For billable outcomes, record dsp_delivery_event and advertiser_spend_ledger.
	// Attribution is always resolved from the persisted line item and campaign
	// rows, never from the notice body, so a spoofed notice cannot bill another
	// advertiser.
	if params.Outcome == OutcomeBillable {
		eventUUID := params.EventID
		if eventUUID == "" {
			eventUUID, _ = domain.NewUUIDv7()
		}
		eventBytes, err := normalizeUUIDBytes(eventUUID)
		if err != nil {
			return fmt.Errorf("invalid event ID: %w", err)
		}
		advBytes, campBytes, liBytes, err := s.resolveAttribution(ctx, tx, params, settledLineItem)
		if err != nil {
			return err
		}
		crBytes := normalizeOptionalUUIDBytes(params.CreativeID)
		occurredAt := params.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = now
		}

		_, err = tx.ExecContext(ctx, `INSERT INTO dsp_delivery_event
			(tenant_id, event_id, source_event_key, event_type, auction_id, bid_id, imp_id,
			 advertiser_id, campaign_id, line_item_id, creative_id, currency, amount_micros, occurred_at, received_at, schema_version)
			VALUES (?, ?, ?, 'billable', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON DUPLICATE KEY UPDATE received_at = VALUES(received_at)`,
			s.tenantID, eventBytes, sourceKeyBytes, params.AuctionID, params.BidID, params.ImpID,
			advBytes, campBytes, liBytes, crBytes, currency, params.ActualAmount, occurredAt, now)
		if err != nil {
			return fmt.Errorf("insert dsp delivery event: %w", err)
		}

		entryUUID, _ := domain.NewUUIDv7()
		entryBytes, _ := hex.DecodeString(entryUUID)
		idempKey := sha256.Sum256([]byte(eventUUID + ":" + params.SourceEventKey))
		// The ledger is append-only: a replay with the same idempotency key is a
		// no-op, never an amount update.
		_, err = tx.ExecContext(ctx, `INSERT INTO advertiser_spend_ledger
			(tenant_id, entry_id, idempotency_key, source_event_id, advertiser_id, campaign_id,
			 line_item_id, entry_type, amount_micros, currency, occurred_at, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'billable', ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE entry_id = entry_id`,
			s.tenantID, entryBytes, idempKey[:], eventBytes, advBytes, campBytes, liBytes,
			params.ActualAmount, currency, occurredAt, now)
		if err != nil {
			return fmt.Errorf("insert advertiser spend ledger: %w", err)
		}
	}

	return tx.Commit()
}

// resolveAttribution returns the advertiser, campaign and line item that own the
// settled reservation. Explicit caller values win; otherwise the persisted
// line_item/campaign rows are authoritative.
func (s *SQLBudgetStore) resolveAttribution(ctx context.Context, tx *sql.Tx, params SettlementParams, lineItemBytes []byte) (adv, camp, li []byte, err error) {
	if params.LineItemID != "" && params.CampaignID != "" && params.AdvertiserID != "" {
		return normalizeOptionalUUIDBytes(params.AdvertiserID),
			normalizeOptionalUUIDBytes(params.CampaignID),
			normalizeOptionalUUIDBytes(params.LineItemID), nil
	}
	lineItemID := lineItemBytes
	if params.LineItemID != "" {
		lineItemID = normalizeOptionalUUIDBytes(params.LineItemID)
	}
	if lineItemID == nil {
		return nil, nil, nil, fmt.Errorf("%w: no line item to attribute billable spend", ErrInvalidSettlement)
	}
	row := tx.QueryRowContext(ctx, `SELECT l.line_item_id, l.campaign_id, c.advertiser_id
		FROM line_item l
		JOIN campaign c ON c.tenant_id = l.tenant_id AND c.campaign_id = l.campaign_id
		WHERE l.tenant_id = ? AND l.line_item_id = ?`, s.tenantID, lineItemID)
	if err := row.Scan(&li, &camp, &adv); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, fmt.Errorf("%w: line item attribution not found", ErrInvalidSettlement)
		}
		return nil, nil, nil, fmt.Errorf("resolve billable attribution: %w", err)
	}
	return adv, camp, li, nil
}

// recordUnderflowQuarantine parks a lease/account drift for reconciliation.
func (s *SQLBudgetStore) recordUnderflowQuarantine(ctx context.Context, tx *sql.Tx, params SettlementParams, scopeID string, landed int64) error {
	resBytes, err := normalizeUUIDBytes(params.ReservationID)
	if err != nil {
		return fmt.Errorf("%w: invalid reservation id", ErrInvalidSettlement)
	}
	quarantineID, err := domain.NewUUIDv7()
	if err != nil {
		return fmt.Errorf("create quarantine id: %w", err)
	}
	quarantineBytes, _ := hex.DecodeString(quarantineID)
	payloadHash := sha256.Sum256([]byte(fmt.Sprintf("leased_underflow:%s:%s:%d", params.ReservationID, scopeID, landed)))
	if _, err := tx.ExecContext(ctx, `INSERT INTO dsp_notice_quarantine
		(tenant_id, quarantine_id, reservation_id, source_event_key, outcome, reason_code, payload_hash, status, received_at)
		VALUES (?, ?, ?, ?, ?, 'leased_underflow', ?, 'pending', NOW(6))
		ON DUPLICATE KEY UPDATE reason_code = VALUES(reason_code)`,
		s.tenantID, quarantineBytes, resBytes, normalize32Bytes(params.SourceEventKey+":"+scopeID),
		string(params.Outcome), payloadHash[:]); err != nil {
		return fmt.Errorf("record lease underflow quarantine: %w", err)
	}
	return nil
}

// materializeLease creates the settleable lease row for a reservation whose
// budget hold lives in Valkey. It keeps the settlement's foreign key and audit
// trail intact without duplicating the runtime lease bookkeeping in MariaDB.
func (s *SQLBudgetStore) materializeLease(ctx context.Context, tx *sql.Tx, params SettlementParams, demand Demand, now time.Time) ([]byte, string, time.Time, []byte, error) {
	lineItemID, scope, ok := splitScopeID(demand.ScopeID)
	if !ok {
		return nil, "", time.Time{}, nil, fmt.Errorf("%w: cannot attribute scope %q", ErrInvalidSettlement, demand.ScopeID)
	}
	lineItemBytes, err := normalizeUUIDBytes(lineItemID)
	if err != nil {
		return nil, "", time.Time{}, nil, fmt.Errorf("%w: invalid line item in scope %q", ErrInvalidSettlement, demand.ScopeID)
	}
	periodStart := normalizePeriodStart(scope, now)
	// The runtime lease id is shared by every reservation of a scope, so the
	// settleable row is keyed by the reservation instead: one row per
	// reservation and scope, which is also what the settlement FK needs.
	leaseBytes := settlementLeaseID(params.ReservationID, demand.ScopeID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO budget_lease
		(tenant_id, lease_id, line_item_id, budget_scope, period_start, owner_instance, amount_micros,
		 held_micros, settlement_grace_until, status, expires_at)
		VALUES (?, ?, ?, ?, ?, 'settlement', ?, ?, ?, 'settled', ?)
		ON DUPLICATE KEY UPDATE held_micros = VALUES(held_micros), status = 'settled'`,
		s.tenantID, leaseBytes, lineItemBytes, scope, periodStart, int64(demand.AmountMicros),
		int64(demand.AmountMicros), now.UTC(), now.UTC()); err != nil {
		return nil, "", time.Time{}, nil, fmt.Errorf("materialize settlement lease: %w", err)
	}
	return lineItemBytes, scope, periodStart, leaseBytes, nil
}

// settlementLeaseID derives the stable lease id used for a settlement whose
// budget hold was tracked in Valkey.
func settlementLeaseID(reservationID, scopeID string) []byte {
	digest := sha256.Sum256([]byte("openads.settlement.lease.v1|" + reservationID + "|" + scopeID))
	return digest[:16]
}

// splitScopeID parses "<lineItemID>:<scope>" into its parts.
func splitScopeID(scopeID string) (string, string, bool) {
	lineItemID, scope, ok := strings.Cut(scopeID, ":")
	if !ok || lineItemID == "" || scope == "" {
		return "", "", false
	}
	return lineItemID, scope, true
}

// WinNotice records a non-billable win for delivery attribution. It never
// touches the budget, the spend ledger or the reservation terminal state.
type WinNotice struct {
	ReservationID string
	LineItemID    string
	AuctionID     string
	BidID         string
	ImpID         string
	CreativeID    string
	Currency      string
	OccurredAt    time.Time
}

// RecordWinNotice persists one non-billable win event. Replays are idempotent.
func (s *SQLBudgetStore) RecordWinNotice(ctx context.Context, notice WinNotice) error {
	if notice.BidID == "" {
		return fmt.Errorf("%w: bid id is required", ErrInvalidSettlement)
	}
	eventUUID, err := domain.NewUUIDv7()
	if err != nil {
		return fmt.Errorf("create win event id: %w", err)
	}
	eventBytes, _ := hex.DecodeString(eventUUID)
	sourceKey := normalize32Bytes("win:" + s.tenantHex() + ":" + notice.BidID)
	occurredAt := notice.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin win notice transaction: %w", err)
	}
	defer tx.Rollback()

	advBytes, campBytes, liBytes, err := s.resolveAttribution(ctx, tx, SettlementParams{
		LineItemID: notice.LineItemID,
	}, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dsp_delivery_event
		(tenant_id, event_id, source_event_key, event_type, auction_id, bid_id, imp_id,
		 advertiser_id, campaign_id, line_item_id, creative_id, currency, amount_micros,
		 occurred_at, received_at, schema_version)
		VALUES (?, ?, ?, 'win', ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, 1)
		ON DUPLICATE KEY UPDATE received_at = VALUES(received_at)`,
		s.tenantID, eventBytes, sourceKey, notice.AuctionID, notice.BidID, notice.ImpID,
		advBytes, campBytes, liBytes, normalizeOptionalUUIDBytes(notice.CreativeID),
		notice.Currency, occurredAt, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert win delivery event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit win notice: %w", err)
	}
	return nil
}

func (s *SQLBudgetStore) tenantHex() string {
	return hex.EncodeToString(s.tenantID)
}

// quarantineLateNotice parks a notice that arrived after the settlement grace so
// a finance operator can decide on it instead of silently dropping the money.
func (s *SQLBudgetStore) quarantineLateNotice(ctx context.Context, params SettlementParams, reason string) {
	resBytes, err := normalizeUUIDBytes(params.ReservationID)
	if err != nil {
		return
	}
	quarantineID, err := domain.NewUUIDv7()
	if err != nil {
		return
	}
	quarantineBytes, _ := hex.DecodeString(quarantineID)
	payloadHash := sha256.Sum256([]byte(string(params.Outcome) + ":" + params.ReservationID + ":" + params.SourceEventKey))
	_, _ = s.db.ExecContext(ctx, `INSERT INTO dsp_notice_quarantine
		(tenant_id, quarantine_id, reservation_id, source_event_key, outcome, reason_code, payload_hash, status, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', NOW(6))
		ON DUPLICATE KEY UPDATE reason_code = VALUES(reason_code)`,
		s.tenantID, quarantineBytes, resBytes, normalize32Bytes(params.SourceEventKey),
		string(params.Outcome), reason, payloadHash[:])
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
