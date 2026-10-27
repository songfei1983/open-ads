package internalapi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/internalapi"
)

// TestIdempotencyStoreRoundTrips exercises the ClickHouse-backed store used in
// production; it is skipped without a reachable ClickHouse.
func TestIdempotencyStoreRoundTrips(t *testing.T) {
	addr := os.Getenv("DMP_CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("DMP_CLICKHOUSE_TEST_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := control.OpenClickHouse(ctx, addr,
		testEnvOr("DMP_CLICKHOUSE_TEST_USER", "dmp_user"),
		testEnvOr("DMP_CLICKHOUSE_TEST_PASSWORD", "dmp_clickhouse_password"),
		testEnvOr("DMP_CLICKHOUSE_TEST_DATABASE", "dmp"))
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	store := internalapi.NewIdempotencyStore(conn, uuid.New())
	key := "test-key-" + uuid.NewString()

	if _, found, err := store.Lookup(ctx, key, "snapshots:create"); err != nil || found {
		t.Fatalf("an unused key must not be found: found=%v err=%v", found, err)
	}

	record := internalapi.IdempotencyRecord{
		Key: key, Route: "snapshots:create", RequestHash: "hash-1",
		Status: 201, Body: `{"snapshot_id":"01a17d00-0000-7000-8000-000000000001"}`,
	}
	if err := store.Save(ctx, record); err != nil {
		t.Fatalf("save: %v", err)
	}

	stored, found, err := store.Lookup(ctx, key, "snapshots:create")
	if err != nil || !found {
		t.Fatalf("stored key must be found: found=%v err=%v", found, err)
	}
	if stored.Status != 201 || stored.RequestHash != "hash-1" || stored.Body != record.Body {
		t.Fatalf("stored record lost data: %+v", stored)
	}
	if stored.RecordedAt.IsZero() {
		t.Fatal("stored record is missing its timestamp")
	}

	// Keys are scoped per route: the same key on another route is unknown.
	if _, found, err := store.Lookup(ctx, key, "identity:revoke"); err != nil || found {
		t.Fatalf("keys must not leak across routes: found=%v err=%v", found, err)
	}
}

func testEnvOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
