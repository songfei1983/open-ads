package snapshot_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
)

func newIntegrationStore(t *testing.T) (*snapshot.Store, *control.ClickHouseStore, uuid.UUID) {
	t.Helper()
	addr := os.Getenv("DMP_CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("DMP_CLICKHOUSE_TEST_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := control.OpenClickHouse(ctx, addr, envOr("DMP_CLICKHOUSE_TEST_USER", "dmp_user"),
		envOr("DMP_CLICKHOUSE_TEST_PASSWORD", "dmp_clickhouse_password"), envOr("DMP_CLICKHOUSE_TEST_DATABASE", "dmp"))
	if err != nil {
		t.Fatalf("open clickhouse: %v", err)
	}
	tenant := uuid.New()
	defs := control.NewClickHouseStore(conn, tenant)
	t.Cleanup(func() { _ = defs.Close() })
	return snapshot.NewStore(conn, defs, tenant), defs, tenant
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func seedDefinitions(t *testing.T, defs *control.ClickHouseStore) {
	t.Helper()
	ctx := context.Background()
	meta := control.Mutation{Actor: "snapshot-test", RequestID: "req-snapshot-1"}
	ns := control.NamespaceInput{ID: "publisher-account-v1", DisplayName: "Publisher account",
		HashingAlgorithm: control.HashingAlgorithmHMACSHA256, SubjectKeyVersion: 1, RetentionMonths: 3}
	if _, err := defs.CreateNamespace(ctx, ns, meta); err != nil && !errors.Is(err, control.ErrAlreadyExists) {
		t.Fatalf("seed namespace: %v", err)
	}
	prov := control.ProviderInput{ID: "dmp-provider-01", DisplayName: "Provider",
		Namespace: "publisher-account-v1", LicenseVersion: "lic-1"}
	if _, err := defs.CreateProvider(ctx, prov, meta); err != nil && !errors.Is(err, control.ErrAlreadyExists) {
		t.Fatalf("seed provider: %v", err)
	}
	for _, id := range []string{"seg-tech-enthusiasts", "seg-sports-fans"} {
		seg := control.SegmentInput{ProviderID: "dmp-provider-01", Name: id, ID: id}
		if _, err := defs.CreateSegment(ctx, seg, meta); err != nil && !errors.Is(err, control.ErrAlreadyExists) {
			t.Fatalf("seed segment %s: %v", id, err)
		}
	}
}

func TestSnapshotImportVerification(t *testing.T) {
	store, defs, _ := newIntegrationStore(t)
	seedDefinitions(t, defs)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
		ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1",
		SchemaVersion: 1, SourceVersion: "feed-2026-10-27", ExpectedRows: 3,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	tokenA := "1111111111111111111111111111111111111111111111111111111111111111"
	tokenB := "2222222222222222222222222222222222222222222222222222222222222222"
	result, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: tokenA, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts", "seg-sports-fans"}},
		{SubjectToken: tokenB, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}},
	})
	if err != nil {
		t.Fatalf("append rows: %v", err)
	}
	if result.StoredRows != 3 || result.DuplicateRows != 0 {
		t.Fatalf("unexpected append result: %+v", result)
	}

	// Re-delivery is idempotent: nothing new is stored.
	result, err = store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: tokenA, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts", "seg-sports-fans"}},
		{SubjectToken: tokenB, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}},
	})
	if err != nil {
		t.Fatalf("re-append rows: %v", err)
	}
	if result.StoredRows != 0 || result.IdempotentRows != 3 {
		t.Fatalf("re-delivery must be idempotent: %+v", result)
	}

	verified, err := store.Verify(ctx, job.SnapshotID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified.Status != snapshot.StatusVerified || verified.StoredRows != 3 {
		t.Fatalf("unexpected verified job: %+v", verified)
	}
	count, checksum, err := store.ChecksumOf(ctx, job.SnapshotID)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if count != 3 || len(checksum) != 64 {
		t.Fatalf("unexpected checksum result: %d %q", count, checksum)
	}

	// A verified job no longer accepts rows.
	if _, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: tokenB, SubjectKeyVersion: 2, SegmentIDs: []string{"seg-tech-enthusiasts"}},
	}); !errors.Is(err, snapshot.ErrJobState) {
		t.Fatalf("verified job must reject new rows, got %v", err)
	}
}

func TestSnapshotImportRejectsRawIdentityAndUnknownSegments(t *testing.T) {
	store, defs, _ := newIntegrationStore(t)
	seedDefinitions(t, defs)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
		ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1",
		SchemaVersion: 1, SourceVersion: "feed-2026-10-27", ExpectedRows: 1,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if _, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: "alice@example.com", SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}},
	}); !errors.Is(err, snapshot.ErrInvalidImport) {
		t.Fatalf("raw identity must be rejected, got %v", err)
	}

	if _, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: "3333333333333333333333333333333333333333333333333333333333333333", SubjectKeyVersion: 1,
			SegmentIDs: []string{"seg-unknown"}},
	}); !errors.Is(err, snapshot.ErrInvalidImport) {
		t.Fatalf("unknown segment must be rejected, got %v", err)
	}

	rows, err := store.Rows(ctx, job.SnapshotID)
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rejected batches must store nothing, got %+v", rows)
	}
}

func TestSnapshotVerificationFailures(t *testing.T) {
	store, defs, _ := newIntegrationStore(t)
	seedDefinitions(t, defs)
	ctx := context.Background()
	token := "4444444444444444444444444444444444444444444444444444444444444444"

	t.Run("checksum mismatch", func(t *testing.T) {
		job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
			ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1", SchemaVersion: 1,
			SourceVersion: "feed-checksum", ExpectedRows: 1,
			ExpectedChecksum: "0000000000000000000000000000000000000000000000000000000000000000",
		})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		if _, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
			{SubjectToken: token, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
		failed, err := store.Verify(ctx, job.SnapshotID)
		if !errors.Is(err, snapshot.ErrVerification) {
			t.Fatalf("checksum mismatch must fail verification, got %v", err)
		}
		if failed.Status != snapshot.StatusFailed || failed.FailureReason == "" {
			t.Fatalf("failure must be recorded: %+v", failed)
		}
	})

	t.Run("row count mismatch", func(t *testing.T) {
		job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
			ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1", SchemaVersion: 1,
			SourceVersion: "feed-rows", ExpectedRows: 9,
		})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		if _, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
			{SubjectToken: token, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
		if _, err := store.Verify(ctx, job.SnapshotID); !errors.Is(err, snapshot.ErrVerification) {
			t.Fatalf("row count mismatch must fail verification, got %v", err)
		}
	})

	t.Run("duplicate rate", func(t *testing.T) {
		job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
			ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1", SchemaVersion: 1,
			SourceVersion: "feed-dupes", ExpectedRows: 1,
		})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		// The same key repeated inside one batch is counted as a duplicate and
		// pushes the rate over the data-quality ceiling.
		rows := make([]snapshot.MemberRow, 0, 21)
		for i := 0; i < 21; i++ {
			rows = append(rows, snapshot.MemberRow{SubjectToken: token, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}})
		}
		if _, err := store.AppendRows(ctx, job.SnapshotID, rows); err != nil {
			t.Fatalf("append: %v", err)
		}
		if _, err := store.Verify(ctx, job.SnapshotID); !errors.Is(err, snapshot.ErrVerification) {
			t.Fatalf("duplicate rate must fail verification, got %v", err)
		}
	})
}

func TestSnapshotPublishAndResolve(t *testing.T) {
	store, defs, _ := newIntegrationStore(t)
	seedDefinitions(t, defs)
	ctx := context.Background()
	token := "5555555555555555555555555555555555555555555555555555555555555555"

	job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
		ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1",
		SchemaVersion: 1, SourceVersion: "feed-publish", ExpectedRows: 2,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.AppendRows(ctx, job.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: token, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts", "seg-sports-fans"}},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := store.Verify(ctx, job.SnapshotID); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// A verified but unpublished snapshot must not resolve.
	if _, err := store.Resolve(ctx, snapshot.ResolveInput{
		Namespace: "publisher-account-v1", ProviderID: "dmp-provider-01", SubjectToken: token,
	}); !errors.Is(err, snapshot.ErrNoPublication) {
		t.Fatalf("unpublished snapshot must not resolve, got %v", err)
	}

	published, err := store.Publish(ctx, job.SnapshotID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != snapshot.StatusPublished {
		t.Fatalf("unexpected publish state: %+v", published)
	}
	if again, err := store.Publish(ctx, job.SnapshotID); err != nil || again.Status != snapshot.StatusPublished {
		t.Fatalf("publish must be idempotent: %v %+v", err, again)
	}

	result, err := store.Resolve(ctx, snapshot.ResolveInput{
		RequestID: "req-resolve-1", Namespace: "publisher-account-v1", ProviderID: "dmp-provider-01",
		SubjectToken: token, MaxSegments: 1,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.SnapshotID != job.SnapshotID || len(result.Segments) != 1 {
		t.Fatalf("unexpected resolve result: %+v", result)
	}

	// An unknown subject resolves to an empty segment list, not an error.
	other := "6666666666666666666666666666666666666666666666666666666666666666"
	empty, err := store.Resolve(ctx, snapshot.ResolveInput{
		Namespace: "publisher-account-v1", ProviderID: "dmp-provider-01", SubjectToken: other,
	})
	if err != nil || len(empty.Segments) != 0 {
		t.Fatalf("unknown subject must resolve empty: %v %+v", err, empty)
	}

	// A newer publication becomes the serving snapshot.
	second, err := store.CreateJob(ctx, snapshot.CreateJobInput{
		ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1",
		SchemaVersion: 1, SourceVersion: "feed-publish-2", ExpectedRows: 1,
	})
	if err != nil {
		t.Fatalf("create second job: %v", err)
	}
	if _, err := store.AppendRows(ctx, second.SnapshotID, []snapshot.MemberRow{
		{SubjectToken: other, SubjectKeyVersion: 1, SegmentIDs: []string{"seg-tech-enthusiasts"}},
	}); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if _, err := store.Verify(ctx, second.SnapshotID); err != nil {
		t.Fatalf("verify second: %v", err)
	}
	if _, err := store.Publish(ctx, second.SnapshotID); err != nil {
		t.Fatalf("publish second: %v", err)
	}
	current, err := store.CurrentPublication(ctx, "dmp-provider-01", "publisher-account-v1")
	if err != nil || current.SnapshotID != second.SnapshotID {
		t.Fatalf("current publication did not move: %v %+v", err, current)
	}
	moved, err := store.Resolve(ctx, snapshot.ResolveInput{
		Namespace: "publisher-account-v1", ProviderID: "dmp-provider-01", SubjectToken: other,
	})
	if err != nil || len(moved.Segments) != 1 {
		t.Fatalf("new publication must serve its rows: %v %+v", err, moved)
	}

	// The first snapshot is still pinned-resolvable because it stays published.
	pinned, err := store.Resolve(ctx, snapshot.ResolveInput{
		Namespace: "publisher-account-v1", ProviderID: "dmp-provider-01", SubjectToken: token,
		SnapshotID: job.SnapshotID,
	})
	if err != nil || len(pinned.Segments) != 2 {
		t.Fatalf("published snapshot must stay resolvable by id: %v %+v", err, pinned)
	}
}

func TestSnapshotPublishRequiresVerification(t *testing.T) {
	store, defs, _ := newIntegrationStore(t)
	seedDefinitions(t, defs)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, snapshot.CreateJobInput{
		ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1",
		SchemaVersion: 1, SourceVersion: "feed-unverified-publish", ExpectedRows: 1,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.Publish(ctx, job.SnapshotID); !errors.Is(err, snapshot.ErrNotVerified) {
		t.Fatalf("unverified snapshot must not publish, got %v", err)
	}
}
