package ledger_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/ledger"
)

// newStore connects to a real MariaDB. The test is skipped unless
// SSP_MARIADB_TEST_DSN is set, because the ledger guarantees are enforced by
// unique keys, CHECK constraints and transactions.
func newStore(t *testing.T) (*ledger.Store, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("SSP_MARIADB_TEST_DSN")
	if dsn == "" {
		t.Skip("SSP_MARIADB_TEST_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	tenantBytes := randomBytes(t, 16)
	tenantID := hex.EncodeToString(tenantBytes)
	store, err := ledger.NewStore(db, tenantID)
	if err != nil {
		t.Fatalf("new ledger store: %v", err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM ssp_billable_outbox WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM publisher_payable_audit WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM publisher_payable_ledger WHERE tenant_id = UNHEX(?) AND entry_type = 'reversal'`,
			`DELETE FROM publisher_payable_ledger WHERE tenant_id = UNHEX(?)`,
			`DELETE FROM ssp_delivery_event WHERE tenant_id = UNHEX(?)`,
		} {
			if _, err := db.Exec(query, tenantID); err != nil {
				t.Logf("cleanup %q: %v", query, err)
			}
		}
	})
	return store, db, tenantID
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return buf
}

func randomID(t *testing.T) string {
	return hex.EncodeToString(randomBytes(t, 16))
}

func sampleImpression(t *testing.T) ledger.Impression {
	return ledger.Impression{
		AuctionID:      "auction-1",
		BidID:          "bid-1",
		ImpID:          "1",
		PublisherID:    randomID(t),
		PropertyID:     randomID(t),
		PlacementID:    randomID(t),
		DSPID:          randomID(t),
		CreativeID:     randomID(t),
		Currency:       "USD",
		AmountMicros:   domain.Micros(2500),
		OccurredAt:     time.Now().UTC(),
		ReservationRef: "reservation-token",
		BURLTarget:     "https://dsp.example.com/rtb/v1/billable/bid-1",
		PolicyVersion:  3,
	}
}

func TestRecordImpressionIsIdempotent(t *testing.T) {
	store, db, tenantID := newStore(t)
	ctx := context.Background()
	imp := sampleImpression(t)

	first, err := store.RecordImpression(ctx, imp)
	if err != nil {
		t.Fatalf("record impression: %v", err)
	}
	if first.Status != "accepted" || first.EventID == "" {
		t.Fatalf("first result = %+v, want accepted with an event id", first)
	}

	second, err := store.RecordImpression(ctx, imp)
	if err != nil {
		t.Fatalf("replay impression: %v", err)
	}
	if second.Status != "duplicate" || second.EventID != first.EventID {
		t.Fatalf("replay result = %+v, want duplicate of %s", second, first.EventID)
	}

	counts := map[string]int{}
	for _, table := range []string{"ssp_delivery_event", "publisher_payable_ledger", "ssp_billable_outbox"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE tenant_id = UNHEX(?)`, tenantID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	if counts["ssp_delivery_event"] != 1 || counts["publisher_payable_ledger"] != 1 || counts["ssp_billable_outbox"] != 1 {
		t.Fatalf("unexpected row counts after replay: %+v", counts)
	}

	entries, err := store.ListPayable(ctx, 10)
	if err != nil {
		t.Fatalf("list payable: %v", err)
	}
	if len(entries) != 1 || entries[0].EntryType != "billable" || entries[0].AmountMicros != 2500 {
		t.Fatalf("payable entries = %+v, want one billable entry of 2500", entries)
	}
	if entries[0].AuctionID != imp.AuctionID {
		t.Fatalf("payable auction = %q, want %q", entries[0].AuctionID, imp.AuctionID)
	}
}

func TestOutboxLifecycleAndResolution(t *testing.T) {
	store, db, tenantID := newStore(t)
	ctx := context.Background()
	if _, err := store.RecordImpression(ctx, sampleImpression(t)); err != nil {
		t.Fatalf("record impression: %v", err)
	}

	items, err := store.ClaimDue(ctx, time.Now().UTC().Add(time.Second), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim due: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("claimed %d items, want 1", len(items))
	}
	item := items[0]
	if item.AttemptCount != 0 {
		t.Fatalf("attempt count before claim = %d, want 0", item.AttemptCount)
	}

	view := func() ledger.OutboxView {
		t.Helper()
		views, err := store.ListOutbox(ctx, "", 5)
		if err != nil {
			t.Fatalf("list outbox: %v", err)
		}
		if len(views) != 1 {
			t.Fatalf("outbox rows = %d, want 1", len(views))
		}
		return views[0]
	}

	if got := view(); got.Status != ledger.OutboxSending || got.AttemptCount != 1 {
		t.Fatalf("after claim status = %s/%d, want sending/1", got.Status, got.AttemptCount)
	}
	if got := view(); got.TargetHost != "dsp.example.com" {
		t.Fatalf("target host = %q, want dsp.example.com", got.TargetHost)
	}

	if err := store.MarkRetry(ctx, item.EventID, 503, "transport_error", time.Now().UTC()); err != nil {
		t.Fatalf("mark retry: %v", err)
	}
	if got := view(); got.Status != ledger.OutboxPending || got.LastHTTPStatus != 503 {
		t.Fatalf("after retry status = %s/%d, want pending/503", got.Status, got.LastHTTPStatus)
	}

	if err := store.MarkQuarantined(ctx, item.EventID, 400, "http_400"); err != nil {
		t.Fatalf("mark quarantined: %v", err)
	}
	if got := view(); got.Status != ledger.OutboxQuarantined {
		t.Fatalf("after quarantine status = %s, want quarantined", got.Status)
	}
	if err := store.ResolveOutbox(ctx, item.EventID, false, "operator rejected the notice"); err != nil {
		t.Fatalf("resolve outbox: %v", err)
	}
	resolved := view()
	if resolved.Status != ledger.OutboxRejected || resolved.ResolutionText == "" || resolved.ResolvedAt == "" {
		t.Fatalf("resolved row = %+v, want rejected with reason and timestamp", resolved)
	}

	// A resolved row is no longer claimable.
	again, err := store.ClaimDue(ctx, time.Now().UTC().Add(time.Hour), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim after resolution: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("claimed %d resolved rows, want 0", len(again))
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ssp_billable_outbox WHERE tenant_id = UNHEX(?) AND status = 'rejected'`, tenantID).Scan(&count); err != nil {
		t.Fatalf("count rejected: %v", err)
	}
	if count != 1 {
		t.Fatalf("rejected rows = %d, want 1", count)
	}
}

func TestAppendPayableReversalEnforcesAppendOnlyRules(t *testing.T) {
	store, _, _ := newStore(t)
	ctx := context.Background()
	if _, err := store.RecordImpression(ctx, sampleImpression(t)); err != nil {
		t.Fatalf("record impression: %v", err)
	}
	entries, err := store.ListPayable(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list payable: %v (%d entries)", err, len(entries))
	}
	original := entries[0]

	partialID, err := store.AppendPayableReversal(ctx, ledger.Reversal{EntryID: original.EntryID, AmountMicros: 1000, Reason: "partial refund"})
	if err != nil {
		t.Fatalf("partial reversal: %v", err)
	}
	replayID, err := store.AppendPayableReversal(ctx, ledger.Reversal{EntryID: original.EntryID, AmountMicros: 1000, Reason: "partial refund"})
	if err != nil {
		t.Fatalf("replayed reversal: %v", err)
	}
	if replayID != partialID {
		t.Fatalf("replayed reversal returned %s, want %s", replayID, partialID)
	}

	if _, err := store.AppendPayableReversal(ctx, ledger.Reversal{EntryID: original.EntryID, AmountMicros: 2000, Reason: "over-refund"}); !errors.Is(err, ledger.ErrReversalRejected) {
		t.Fatalf("over reversal error = %v, want ErrReversalRejected", err)
	}
	if _, err := store.AppendPayableReversal(ctx, ledger.Reversal{EntryID: randomID(t), AmountMicros: 1, Reason: "missing"}); !errors.Is(err, ledger.ErrEntryNotFound) {
		t.Fatalf("missing entry error = %v, want ErrEntryNotFound", err)
	}

	reversal, err := store.AppendPayableReversal(ctx, ledger.Reversal{EntryID: original.EntryID, AmountMicros: 1500, Reason: "final refund"})
	if err != nil {
		t.Fatalf("final reversal: %v", err)
	}
	entries, err = store.ListPayable(ctx, 10)
	if err != nil {
		t.Fatalf("list payable after reversals: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("payable rows = %d, want 3 (billable + 2 reversals)", len(entries))
	}
	reversed := int64(0)
	for _, entry := range entries {
		if entry.EntryType == "reversal" {
			reversed += -entry.AmountMicros
			if entry.Reverses != original.EntryID {
				t.Fatalf("reversal %s references %q, want %q", entry.EntryID, entry.Reverses, original.EntryID)
			}
		}
	}
	if reversed != 2500 {
		t.Fatalf("cumulative reversed = %d, want 2500", reversed)
	}
	if _, err := store.AppendPayableReversal(ctx, ledger.Reversal{EntryID: reversal, AmountMicros: 1, Reason: "chained"}); !errors.Is(err, ledger.ErrReversalRejected) {
		t.Fatalf("chained reversal error = %v, want ErrReversalRejected", err)
	}
}

func TestAdminHandlerRoutesLedgerEndpoints(t *testing.T) {
	store, _, _ := newStore(t)
	ctx := context.Background()
	imp := sampleImpression(t)
	imp.BURLTarget = ""
	if _, err := store.RecordImpression(ctx, imp); err != nil {
		t.Fatalf("record impression: %v", err)
	}
	entries, err := store.ListPayable(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list payable: %v (%d entries)", err, len(entries))
	}
	entryID := entries[0].EntryID

	handler := ledger.NewAdminHandler(store, "admin-secret")
	do := func(method, path, body string, authorize bool) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorize {
			request.Header.Set("Authorization", "Bearer admin-secret")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	if code := do(http.MethodGet, "/admin/v1/publisher-payable", "", false).Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", code)
	}
	if code := do(http.MethodGet, "/admin/v1/publisher-payable", "", true).Code; code != http.StatusOK {
		t.Fatalf("list payable = %d, want 200", code)
	}
	reversal := do(http.MethodPost, "/admin/v1/publisher-payable/"+entryID+":reversal",
		`{"amount_micros":1000,"reason":"partial"}`, true)
	if reversal.Code != http.StatusCreated {
		t.Fatalf("reversal = %d (%s), want 201", reversal.Code, reversal.Body.String())
	}
	replay := do(http.MethodPost, "/admin/v1/publisher-payable/"+entryID+":reversal",
		`{"amount_micros":1000,"reason":"partial"}`, true)
	if replay.Code != http.StatusCreated || replay.Body.String() != reversal.Body.String() {
		t.Fatalf("replayed reversal = %d (%s), want the first entry", replay.Code, replay.Body.String())
	}
	if over := do(http.MethodPost, "/admin/v1/publisher-payable/"+entryID+":reversal",
		`{"amount_micros":9000,"reason":"too much"}`, true); over.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over reversal = %d, want 422", over.Code)
	}
	if code := do(http.MethodGet, "/admin/v1/billable-outbox?limit=5", "", true).Code; code != http.StatusOK {
		t.Fatalf("list outbox = %d, want 200", code)
	}
	if code := do(http.MethodGet, "/admin/v1/publisher-payable/audit?limit=5", "", true).Code; code != http.StatusOK {
		t.Fatalf("list audit = %d, want 200", code)
	}
	if code := do(http.MethodGet, "/admin/v1/publisher-payable/export", "", true).Code; code != http.StatusOK {
		t.Fatalf("export = %d, want 200", code)
	}
	if code := do(http.MethodGet, "/admin/v1/publisher-payable/export?from=not-a-time", "", true).Code; code != http.StatusBadRequest {
		t.Fatalf("invalid export range = %d, want 400", code)
	}
	if code := do(http.MethodGet, "/admin/v1/publisher-payable/unknown", "", true).Code; code != http.StatusNotFound {
		t.Fatalf("unknown route = %d, want 404", code)
	}
}

func TestPayableAuditAndExport(t *testing.T) {
	store, _, _ := newStore(t)
	ctx := context.Background()
	imp := sampleImpression(t)
	if _, err := store.RecordImpression(ctx, imp); err != nil {
		t.Fatalf("record impression: %v", err)
	}
	entries, err := store.ListPayable(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list payable: %v (%d entries)", err, len(entries))
	}
	original := entries[0]

	if _, err := store.AppendPayableReversal(ctx, ledger.Reversal{
		EntryID: original.EntryID, AmountMicros: 1000, Reason: "operator correction",
		Actor: "finance.sandbox", RequestID: "req-42",
	}); err != nil {
		t.Fatalf("append reversal: %v", err)
	}

	audit, err := store.ListPayableAudit(ctx, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(audit) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(audit))
	}
	if audit[0].Action != "reversal" || audit[0].AmountMicros != -1000 ||
		audit[0].Actor != "finance.sandbox" || audit[0].RequestID != "req-42" ||
		audit[0].Reverses != original.EntryID {
		t.Fatalf("audit row = %+v", audit[0])
	}

	summary, err := store.ExportPayable(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("export payable: %v", err)
	}
	if len(summary.Totals) != 1 {
		t.Fatalf("export totals = %d, want 1", len(summary.Totals))
	}
	total := summary.Totals[0]
	if total.BillableMicros != 2500 || total.ReversalMicros != 1000 || total.NetPayableMicros != 1500 {
		t.Fatalf("export total = %+v, want billable 2500 / reversal 1000 / net 1500", total)
	}
	if total.PublisherID != imp.PublisherID {
		t.Fatalf("export publisher = %q, want %q", total.PublisherID, imp.PublisherID)
	}

	if _, err := store.ExportPayable(ctx, time.Now().Add(time.Hour), time.Now()); !errors.Is(err, ledger.ErrExportRange) {
		t.Fatalf("inverted range error = %v, want ErrExportRange", err)
	}
}
