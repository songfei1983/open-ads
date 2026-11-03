package lease_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/lease"
)

// newIntegrationDB connects to a real MariaDB instance. The test is skipped
// unless DSP_MARIADB_TEST_DSN is set, because the settlement path only makes
// sense against the real constraints and transactions.
func newIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DSP_MARIADB_TEST_DSN")
	if dsn == "" {
		t.Skip("DSP_MARIADB_TEST_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	return db
}

func randomHex(t *testing.T, size int) []byte {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return buf
}

func randomUUIDString(t *testing.T) string {
	return hex.EncodeToString(randomHex(t, 16))
}

// seedLineItem creates the tenant/advertiser/campaign/line item rows a
// settlement needs to attribute spend, and removes them afterwards.
func seedLineItem(t *testing.T, ctx context.Context, db *sql.DB, tenant, advertiser, campaign, lineItem []byte, currency string) {
	t.Helper()
	now := time.Now().UTC()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tenant (tenant_id, display_name, status, default_currency, created_at, updated_at, version)
			VALUES (?, 'settle-integration', 'active', ?, ?, ?, 1)`, []any{tenant, currency, now, now}},
		{`INSERT INTO advertiser (tenant_id, advertiser_id, status, default_currency, version, updated_at)
			VALUES (?, ?, 'active', ?, 1, ?)`, []any{tenant, advertiser, currency, now}},
		{`INSERT INTO campaign (tenant_id, campaign_id, advertiser_id, status, starts_at, version, updated_at)
			VALUES (?, ?, ?, 'active', ?, 1, ?)`, []any{tenant, campaign, advertiser, now.Add(-time.Hour), now}},
		{`INSERT INTO line_item (tenant_id, line_item_id, campaign_id, status, currency, bid_cpm_micros,
			daily_budget_micros, lifetime_budget_micros, targeting_version, version, updated_at)
			VALUES (?, ?, ?, 'active', ?, 4200000, 100000000, 1000000000, 1, 1, ?)`,
			[]any{tenant, lineItem, campaign, currency, now}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	t.Cleanup(func() {
		tenantHex := hex.EncodeToString(tenant)
		cleanup := []string{
			`DELETE FROM dsp_notice_quarantine WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM advertiser_spend_ledger WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM dsp_delivery_event WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM budget_reservation_settlement WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM budget_lease WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM budget_account WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM line_item WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM campaign WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM advertiser WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM tenant WHERE tenant_id = UNHEX(?)`,
		}
		for _, query := range cleanup {
			if _, err := db.Exec(query, tenantHex); err != nil {
				t.Logf("cleanup %q: %v", query, err)
			}
		}
	})
}

func TestSettleReservationPersistsBillableSpend(t *testing.T) {
	db := newIntegrationDB(t)
	ctx := context.Background()

	tenantBytes := randomHex(t, 16)
	advertiserBytes := randomHex(t, 16)
	campaignBytes := randomHex(t, 16)
	lineItemBytes := randomHex(t, 16)
	seedLineItem(t, ctx, db, tenantBytes, advertiserBytes, campaignBytes, lineItemBytes, "USD")

	tenantID := hex.EncodeToString(tenantBytes)
	lineItemID := hex.EncodeToString(lineItemBytes)
	store, err := lease.NewSQLBudgetStore(db, tenantID)
	if err != nil {
		t.Fatalf("new budget store: %v", err)
	}
	now := time.Now().UTC()
	if err := store.EnsureAccount(ctx, lineItemID, "daily", now, "USD", 10_000_000); err != nil {
		t.Fatalf("ensure account: %v", err)
	}
	grant, err := store.AllocateLease(ctx, lineItemID, "daily", now, "settle-test", 2500, 30*time.Second)
	if err != nil {
		t.Fatalf("allocate lease: %v", err)
	}

	params := lease.SettlementParams{
		TenantID:       tenantID,
		ReservationID:  randomUUIDString(t),
		Outcome:        lease.OutcomeBillable,
		ActualAmount:   domain.Micros(2500),
		Currency:       "USD",
		SourceEventKey: hex.EncodeToString(randomHex(t, 32)),
		Demands:        []lease.Demand{{ScopeID: grant.ScopeID, AmountMicros: 2500}},
		LeaseIDs:       map[string]string{grant.ScopeID: grant.LeaseID},
		EventID:        randomUUIDString(t),
		AuctionID:      "auction-settle-1",
		BidID:          "bid-settle-1",
		ImpID:          "1",
		OccurredAt:     now,
	}
	if err := store.SettleReservation(ctx, params); err != nil {
		t.Fatalf("settle billable reservation: %v", err)
	}

	assertSpend := func(wantAmount int64, wantEntries int) {
		t.Helper()
		var entries int
		var amount int64
		var advertiserRead, campaignRead, lineItemRead []byte
		var entryType string
		row := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM advertiser_spend_ledger WHERE tenant_id = ?`, tenantBytes)
		if err := row.Scan(&entries); err != nil {
			t.Fatalf("count spend ledger: %v", err)
		}
		if entries != wantEntries {
			t.Fatalf("spend ledger entries = %d, want %d", entries, wantEntries)
		}
		row = db.QueryRowContext(ctx, `SELECT amount_micros, advertiser_id, campaign_id, line_item_id, entry_type
			FROM advertiser_spend_ledger WHERE tenant_id = ?`, tenantBytes)
		if err := row.Scan(&amount, &advertiserRead, &campaignRead, &lineItemRead, &entryType); err != nil {
			t.Fatalf("read spend ledger: %v", err)
		}
		if amount != wantAmount {
			t.Fatalf("spend amount = %d, want %d", amount, wantAmount)
		}
		if entryType != "billable" {
			t.Fatalf("entry type = %q, want billable", entryType)
		}
		if hex.EncodeToString(advertiserRead) != hex.EncodeToString(advertiserBytes) ||
			hex.EncodeToString(campaignRead) != hex.EncodeToString(campaignBytes) ||
			hex.EncodeToString(lineItemRead) != hex.EncodeToString(lineItemBytes) {
			t.Fatalf("spend attribution does not match the seeded line item")
		}
		var deliveries, settlements int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dsp_delivery_event WHERE tenant_id = ?`, tenantBytes).Scan(&deliveries); err != nil {
			t.Fatalf("count delivery events: %v", err)
		}
		if deliveries != 1 {
			t.Fatalf("delivery events = %d, want 1", deliveries)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budget_reservation_settlement WHERE tenant_id = ?`, tenantBytes).Scan(&settlements); err != nil {
			t.Fatalf("count settlements: %v", err)
		}
		if settlements != 1 {
			t.Fatalf("settlements = %d, want 1", settlements)
		}
	}

	assertSpend(2500, 1)

	// A replay with the same terminal state must be an idempotent success.
	if err := store.SettleReservation(ctx, params); err != nil {
		t.Fatalf("replay billable settlement: %v", err)
	}
	assertSpend(2500, 1)

	// Spend and the leased hold must both leave the account.
	var spent, leased int64
	if err := db.QueryRowContext(ctx, `SELECT spent_micros, leased_micros FROM budget_account
		WHERE tenant_id = ? AND line_item_id = ? AND budget_scope = 'daily'`, tenantBytes, lineItemBytes).Scan(&spent, &leased); err != nil {
		t.Fatalf("read budget account: %v", err)
	}
	if spent != 2500 {
		t.Fatalf("spent_micros = %d, want 2500", spent)
	}
	if leased != 0 {
		t.Fatalf("leased_micros = %d, want 0", leased)
	}
}

func TestSettleReservationQuarantinesConflictingOutcome(t *testing.T) {
	db := newIntegrationDB(t)
	ctx := context.Background()

	tenantBytes := randomHex(t, 16)
	advertiserBytes := randomHex(t, 16)
	campaignBytes := randomHex(t, 16)
	lineItemBytes := randomHex(t, 16)
	seedLineItem(t, ctx, db, tenantBytes, advertiserBytes, campaignBytes, lineItemBytes, "USD")

	tenantID := hex.EncodeToString(tenantBytes)
	lineItemID := hex.EncodeToString(lineItemBytes)
	store, err := lease.NewSQLBudgetStore(db, tenantID)
	if err != nil {
		t.Fatalf("new budget store: %v", err)
	}
	now := time.Now().UTC()
	if err := store.EnsureAccount(ctx, lineItemID, "daily", now, "USD", 10_000_000); err != nil {
		t.Fatalf("ensure account: %v", err)
	}
	grant, err := store.AllocateLease(ctx, lineItemID, "daily", now, "settle-test", 2500, 30*time.Second)
	if err != nil {
		t.Fatalf("allocate lease: %v", err)
	}

	base := lease.SettlementParams{
		TenantID:       tenantID,
		ReservationID:  randomUUIDString(t),
		Outcome:        lease.OutcomeBillable,
		ActualAmount:   domain.Micros(2500),
		Currency:       "USD",
		SourceEventKey: hex.EncodeToString(randomHex(t, 32)),
		Demands:        []lease.Demand{{ScopeID: grant.ScopeID, AmountMicros: 2500}},
		LeaseIDs:       map[string]string{grant.ScopeID: grant.LeaseID},
		EventID:        randomUUIDString(t),
		AuctionID:      "auction-settle-2",
		BidID:          "bid-settle-2",
		ImpID:          "1",
		OccurredAt:     now,
	}
	if err := store.SettleReservation(ctx, base); err != nil {
		t.Fatalf("settle billable reservation: %v", err)
	}

	conflicting := base
	conflicting.Outcome = lease.OutcomeLoss
	conflicting.ActualAmount = 0
	if err := store.SettleReservation(ctx, conflicting); !errors.Is(err, lease.ErrReservationConflict) {
		t.Fatalf("conflicting settlement error = %v, want ErrReservationConflict", err)
	}

	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dsp_notice_quarantine
		WHERE tenant_id = ? AND status = 'pending'`, tenantBytes).Scan(&pending); err != nil {
		t.Fatalf("count quarantine: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending quarantine rows = %d, want 1", pending)
	}
}
