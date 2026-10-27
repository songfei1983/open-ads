package revocation_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/revocation"
)

func newStore(t *testing.T) *revocation.Store {
	t.Helper()
	addr := os.Getenv("DMP_CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("DMP_CLICKHOUSE_TEST_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := control.OpenClickHouse(ctx, addr,
		envOr("DMP_CLICKHOUSE_TEST_USER", "dmp_user"),
		envOr("DMP_CLICKHOUSE_TEST_PASSWORD", "dmp_clickhouse_password"),
		envOr("DMP_CLICKHOUSE_TEST_DATABASE", "dmp"))
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	store := revocation.NewStore(conn, uuid.New())
	t.Cleanup(func() { _ = conn.Close() })
	return store
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestRevocationTombstonesAndStatus(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	id, err := revocation.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	token := strings.Repeat("ab", 32)
	request := revocation.Request{
		RevocationID: id, Namespace: "publisher-account-v1",
		Tokens:     []revocation.TokenVersion{{Token: token, KeyVersion: 1}, {Token: token, KeyVersion: 2}},
		ReasonCode: "user_request",
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := store.Record(ctx, request); err != nil {
		t.Fatalf("record: %v", err)
	}

	tombstones, err := store.Tombstones(ctx)
	if err != nil {
		t.Fatalf("tombstones: %v", err)
	}
	var found int
	for _, tombstone := range tombstones {
		if tombstone.Token == token && tombstone.Namespace == "publisher-account-v1" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("expected two tombstones, found %d", found)
	}

	status, err := store.AppendStatus(ctx, revocation.Status{
		RevocationID: id, OnlineStatus: revocation.OnlineDenied,
		PhysicalDeleteStatus: revocation.DeletePending, CoveredKeyVersions: []uint16{1, 2},
		GateEpoch: 3, DrainedLookupCount: 1,
	})
	if err != nil {
		t.Fatalf("append status: %v", err)
	}
	if status.StatusVersion != 1 {
		t.Fatalf("unexpected status version: %+v", status)
	}
	second, err := store.AppendStatus(ctx, revocation.Status{
		RevocationID: id, OnlineStatus: revocation.OnlineDenied,
		PhysicalDeleteStatus: revocation.DeleteCompleted, CoveredKeyVersions: []uint16{1, 2},
		GateEpoch: 3, DrainedLookupCount: 0,
	})
	if err != nil || second.StatusVersion != 2 {
		t.Fatalf("second status append: %v %+v", err, second)
	}

	latest, err := store.LatestStatus(ctx, id)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.PhysicalDeleteStatus != revocation.DeleteCompleted || latest.StatusVersion != 2 {
		t.Fatalf("latest status is not the newest: %+v", latest)
	}

	statuses, err := store.ListStatuses(ctx, 10)
	if err != nil || len(statuses) == 0 {
		t.Fatalf("list statuses: %v %+v", err, statuses)
	}

	if _, err := store.LatestStatus(ctx, "01a17d00-0000-7000-8000-0000000000aa"); !errors.Is(err, revocation.ErrNotFound) {
		t.Fatalf("unknown revocation must be ErrNotFound, got %v", err)
	}
}

func TestRevocationRejectsRawIdentity(t *testing.T) {
	request := revocation.Request{Namespace: "ns", Tokens: []revocation.TokenVersion{{Token: "alice@example.com", KeyVersion: 1}}}
	if err := request.Validate(); !errors.Is(err, revocation.ErrInvalidRequest) {
		t.Fatalf("raw identity must be rejected, got %v", err)
	}
}
