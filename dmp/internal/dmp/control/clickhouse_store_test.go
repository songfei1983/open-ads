package control_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
)

// newIntegrationStore connects to a real ClickHouse instance. The test is
// skipped unless DMP_CLICKHOUSE_TEST_ADDR is set, because CI runs DMP unit
// tests without a database and the store's concurrency semantics only make
// sense against ClickHouse itself.
func newIntegrationStore(t *testing.T) *control.ClickHouseStore {
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
	store := control.NewClickHouseStore(conn, uuid.New())
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestClickHouseStoreVersionedDefinitions(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()
	meta := control.Mutation{Actor: "integration-test", RequestID: "req-integration-1"}

	namespace := control.NamespaceInput{
		ID: "publisher-account-v1", DisplayName: "Publisher account",
		HashingAlgorithm: control.HashingAlgorithmHMACSHA256, SubjectKeyVersion: 1, RetentionMonths: 3,
	}
	created, err := store.CreateNamespace(ctx, namespace, meta)
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if created.Version != 1 || created.Status != control.StatusActive {
		t.Fatalf("unexpected created namespace: %+v", created)
	}

	if _, err := store.CreateNamespace(ctx, namespace, meta); !errors.Is(err, control.ErrAlreadyExists) {
		t.Fatalf("duplicate create should fail with ErrAlreadyExists, got %v", err)
	}

	if _, err := store.CreateProvider(ctx, control.ProviderInput{
		ID: "provider-missing-ns", DisplayName: "Provider", Namespace: "absent", LicenseVersion: "lic-1",
	}, meta); !errors.Is(err, control.ErrInvalidDefinition) {
		t.Fatalf("provider must reference an active namespace, got %v", err)
	}

	provider, err := store.CreateProvider(ctx, control.ProviderInput{
		ID: "dmp-provider-01", DisplayName: "Data provider",
		Namespace: "publisher-account-v1", LicenseVersion: "lic-2026.10",
	}, meta)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if provider.Version != 1 {
		t.Fatalf("unexpected provider: %+v", provider)
	}

	if _, err := store.UpdateProvider(ctx, "dmp-provider-01", control.ProviderInput{
		Version: 1, DisplayName: "Data provider", Namespace: "publisher-account-v2", LicenseVersion: "lic-2026.11",
	}, meta); !errors.Is(err, control.ErrImmutableField) {
		t.Fatalf("namespace change must be rejected, got %v", err)
	}

	segment, err := store.CreateSegment(ctx, control.SegmentInput{
		ID: "seg-tech-enthusiasts", ProviderID: "dmp-provider-01", Name: "Tech enthusiasts",
	}, meta)
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}
	if segment.Version != 1 || segment.ValidFrom.IsZero() {
		t.Fatalf("unexpected segment: %+v", segment)
	}

	// Optimistic lock: the second update using version 1 must conflict.
	updated, err := store.UpdateNamespace(ctx, "publisher-account-v1", control.NamespaceInput{
		Version: 1, DisplayName: "Publisher account v2",
		HashingAlgorithm: control.HashingAlgorithmHMACSHA256, SubjectKeyVersion: 2, RetentionMonths: 2,
	}, meta)
	if err != nil {
		t.Fatalf("update namespace: %v", err)
	}
	if updated.Version != 2 || updated.SubjectKeyVersion != 2 {
		t.Fatalf("unexpected updated namespace: %+v", updated)
	}
	if _, err := store.UpdateNamespace(ctx, "publisher-account-v1", control.NamespaceInput{
		Version: 1, DisplayName: "stale",
		HashingAlgorithm: control.HashingAlgorithmHMACSHA256, SubjectKeyVersion: 3, RetentionMonths: 2,
	}, meta); !errors.Is(err, control.ErrVersionConflict) {
		t.Fatalf("stale update must conflict, got %v", err)
	}

	current, err := store.GetNamespace(ctx, "publisher-account-v1")
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if current.Version != 2 || current.DisplayName != "Publisher account v2" {
		t.Fatalf("current version is not the latest: %+v", current)
	}

	namespaces, err := store.ListNamespaces(ctx)
	if err != nil || len(namespaces) != 1 {
		t.Fatalf("list namespaces: %v %+v", err, namespaces)
	}

	entries, err := store.ListAudit(ctx, control.AuditQuery{ResourceType: "namespace", ResourceID: "publisher-account-v1"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	// One accepted create, one duplicate create, one accepted update and one
	// stale update: every attempt is recorded.
	if len(entries) != 4 {
		t.Fatalf("expected 4 audited attempts, got %d: %+v", len(entries), entries)
	}
	if entries[0].Outcome != control.OutcomeConflict {
		t.Fatalf("newest audit entry should be the stale update conflict: %+v", entries[0])
	}
	var conflicts int
	for _, entry := range entries {
		if entry.Actor != "integration-test" || entry.RequestID != "req-integration-1" {
			t.Fatalf("audit entry lost caller context: %+v", entry)
		}
		if entry.Outcome == control.OutcomeConflict {
			conflicts++
		}
	}
	if conflicts != 2 {
		t.Fatalf("conflict was not audited: %+v", entries)
	}
}

func TestClickHouseStoreRejectsMissingDefinition(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()
	meta := control.Mutation{Actor: "integration-test", RequestID: "req-integration-2"}

	if _, err := store.GetNamespace(ctx, "does-not-exist"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("missing namespace should be ErrNotFound, got %v", err)
	}
	if _, err := store.UpdateNamespace(ctx, "does-not-exist", control.NamespaceInput{
		Version: 1, DisplayName: "x", HashingAlgorithm: control.HashingAlgorithmHMACSHA256,
		SubjectKeyVersion: 1, RetentionMonths: 1,
	}, meta); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("update of a missing namespace should be ErrNotFound, got %v", err)
	}

	entries, err := store.ListAudit(ctx, control.AuditQuery{ResourceType: "namespace", ResourceID: "does-not-exist"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) == 0 || entries[0].Outcome != control.OutcomeNotFound {
		t.Fatalf("missing-definition attempt was not audited: %+v", entries)
	}
}
