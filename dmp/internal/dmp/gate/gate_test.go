package gate_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/gate"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/valkey"
)

func newTestGate(t *testing.T) *gate.Gate {
	t.Helper()
	addr := os.Getenv("DMP_VALKEY_TEST_ADDR")
	if addr == "" {
		t.Skip("DMP_VALKEY_TEST_ADDR is not set")
	}
	client := &valkey.Client{
		Address:  addr,
		Username: envOr("DMP_VALKEY_TEST_USER", "dmp"),
		Password: envOr("DMP_VALKEY_TEST_PASSWORD", "dmp_valkey_password"),
		Timeout:  2 * time.Second,
	}
	// A unique tenant per test isolates the gate keyspace.
	tenant := "test-" + time.Now().UTC().Format("20060102150405.000000000")
	g := gate.New(client, tenant)
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := client.Do(ctx, "DEL", "dmp:"+tenant+":gate:epoch"); err != nil {
			t.Logf("cleanup epoch: %v", err)
		}
	})
	return g
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestGateAdmitDenyFinalize(t *testing.T) {
	g := newTestGate(t)
	ctx := context.Background()
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if _, err := g.Epoch(ctx); !errors.Is(err, gate.ErrNotReady) {
		t.Fatalf("fresh gate must not be ready, got %v", err)
	}
	epoch, err := g.EnsureEpoch(ctx, nil)
	if err != nil {
		t.Fatalf("ensure epoch: %v", err)
	}
	if epoch == 0 {
		t.Fatal("epoch must be positive")
	}

	if err := g.Admit(ctx, "ns", token, "lease-1"); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if count, err := g.InFlight(ctx, "ns", token); err != nil || count != 1 {
		t.Fatalf("in-flight: %v %d", err, count)
	}
	if err := g.Finalize(ctx, "ns", token, "lease-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if count, err := g.InFlight(ctx, "ns", token); err != nil || count != 0 {
		t.Fatalf("lease was not released: %v %d", err, count)
	}

	registered, err := g.Deny(ctx, "ns", token, "user_request")
	if err != nil || registered != 0 {
		t.Fatalf("deny: %v %d", err, registered)
	}
	if err := g.Admit(ctx, "ns", token, "lease-2"); !errors.Is(err, gate.ErrSubjectDenied) {
		t.Fatalf("denied subject must not be admitted, got %v", err)
	}

	// Another subject stays unaffected.
	if err := g.Admit(ctx, "ns", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "lease-3"); err != nil {
		t.Fatalf("unrelated subject must be admitted: %v", err)
	}
}

func TestGateFinalizeRejectsAfterRevocation(t *testing.T) {
	g := newTestGate(t)
	ctx := context.Background()
	token := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	if _, err := g.EnsureEpoch(ctx, nil); err != nil {
		t.Fatalf("ensure epoch: %v", err)
	}
	if err := g.Admit(ctx, "ns", token, "lease-1"); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := g.Deny(ctx, "ns", token, "user_request"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	// The in-flight lookup re-checks the barrier before answering.
	if err := g.Finalize(ctx, "ns", token, "lease-1"); !errors.Is(err, gate.ErrSubjectDenied) {
		t.Fatalf("finalize must observe the revocation, got %v", err)
	}
}

func TestGateRebuildFromTombstones(t *testing.T) {
	g := newTestGate(t)
	ctx := context.Background()
	revoked := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	allowed := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	// Simulate a lost keyspace: no epoch, only persisted tombstones.
	epoch, err := g.EnsureEpoch(ctx, []gate.DenyEntry{{Namespace: "ns", Token: revoked, ReasonCode: "user_request"}})
	if err != nil || epoch == 0 {
		t.Fatalf("rebuild: %v", err)
	}
	if err := g.Admit(ctx, "ns", revoked, "lease-1"); !errors.Is(err, gate.ErrSubjectDenied) {
		t.Fatalf("rebuilt deny key must still block, got %v", err)
	}
	if err := g.Admit(ctx, "ns", allowed, "lease-2"); err != nil {
		t.Fatalf("allowed subject must pass after rebuild: %v", err)
	}
	// Re-running EnsureEpoch must be a no-op once the epoch exists.
	if epochAgain, err := g.EnsureEpoch(ctx, nil); err != nil || epochAgain != epoch {
		t.Fatalf("epoch must stay stable: %v %d", err, epochAgain)
	}
}

func TestGateEpochMismatchFailsClosed(t *testing.T) {
	g := newTestGate(t)
	ctx := context.Background()
	if _, err := g.EnsureEpoch(ctx, nil); err != nil {
		t.Fatalf("ensure epoch: %v", err)
	}
	// Removing the epoch while admissions are running must stop admissions.
	if err := g.ResetForTest(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := g.Admit(ctx, "ns", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "lease-1"); err == nil {
		t.Fatal("admission without an epoch must fail")
	}
}
