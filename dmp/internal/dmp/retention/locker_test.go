package retention_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/retention"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/valkey"
)

func TestValkeyLockerIsExclusive(t *testing.T) {
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
	key := "dmp:test-leader-" + time.Now().UTC().Format("20060102150405.000000000") + ":lease"
	ctx := context.Background()
	t.Cleanup(func() { _, _ = client.Do(context.Background(), "DEL", key) })

	first := retention.LockerValkey(client)
	token, ok, err := first.TryAcquire(ctx, key, time.Minute)
	if err != nil || !ok || token == "" {
		t.Fatalf("first acquire must win: %q %v %v", token, ok, err)
	}
	if _, ok, _ := retention.LockerValkey(client).TryAcquire(ctx, key, time.Minute); ok {
		t.Fatal("a held lease must refuse a second holder")
	}

	// A stale holder must not release the lease it no longer owns.
	if err := first.Release(ctx, key, "not-the-token"); err != nil {
		t.Fatalf("stale release must be a no-op: %v", err)
	}
	if _, ok, _ := retention.LockerValkey(client).TryAcquire(ctx, key, time.Minute); ok {
		t.Fatal("a stale release must not free the lease")
	}

	if err := first.Release(ctx, key, token); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if next, ok, err := retention.LockerValkey(client).TryAcquire(ctx, key, time.Minute); err != nil || !ok {
		t.Fatalf("lease must be reusable after release: %q %v", next, err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
