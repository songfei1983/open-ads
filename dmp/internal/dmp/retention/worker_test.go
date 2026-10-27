package retention_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/retention"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/revocation"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
)

type fakeSnapshots struct {
	jobs      []snapshot.Job
	current   map[string]string // provider|namespace -> snapshot id
	deleted   []string
	deleteErr error
}

func (f *fakeSnapshots) ListJobs(context.Context) ([]snapshot.Job, error) { return f.jobs, nil }

func (f *fakeSnapshots) CurrentPublication(_ context.Context, providerID, namespace string) (snapshot.Publication, error) {
	if id, ok := f.current[providerID+"|"+namespace]; ok {
		return snapshot.Publication{ProviderID: providerID, Namespace: namespace, SnapshotID: id}, nil
	}
	return snapshot.Publication{}, snapshot.ErrNoPublication
}

func (f *fakeSnapshots) DeleteSnapshot(_ context.Context, snapshotID string) (uint64, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.deleted = append(f.deleted, snapshotID)
	return 2, nil
}

type fakeRevocations struct {
	statuses []revocation.Status
	tokens   map[string][]revocation.Tombstone
	purged   []string
	appended []revocation.Status
}

func (f *fakeRevocations) ListStatuses(context.Context, int) ([]revocation.Status, error) {
	return f.statuses, nil
}

func (f *fakeRevocations) TokensOf(_ context.Context, revocationID string) ([]revocation.Tombstone, error) {
	return f.tokens[revocationID], nil
}

func (f *fakeRevocations) PurgeSubject(_ context.Context, namespace, token string) (uint64, error) {
	f.purged = append(f.purged, namespace+"/"+token)
	return 1, nil
}

func (f *fakeRevocations) AppendStatus(_ context.Context, status revocation.Status) (revocation.Status, error) {
	f.appended = append(f.appended, status)
	return status, nil
}

func newWorker(t *testing.T, snapshots *fakeSnapshots, revocations *fakeRevocations, now time.Time) *retention.Worker {
	t.Helper()
	worker := retention.NewWorker(snapshots, revocations, retention.Policy{
		MembershipRetention: 90 * 24 * time.Hour,
		EventRetention:      365 * 24 * time.Hour,
		PhysicalDeleteGrace: 5 * time.Minute,
		PhysicalDeleteSLA:   30 * 24 * time.Hour,
	}, nil)
	worker.SetClockForTest(func() time.Time { return now })
	return worker
}

func TestRetentionDeletesExpiredSnapshotsButKeepsTheServingOne(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	snapshots := &fakeSnapshots{
		jobs: []snapshot.Job{
			{SnapshotID: "expired", ProviderID: "p", Namespace: "n", CreatedAt: now.Add(-100 * 24 * time.Hour), Status: snapshot.StatusPublished},
			{SnapshotID: "serving", ProviderID: "p", Namespace: "n", CreatedAt: now.Add(-100 * 24 * time.Hour), Status: snapshot.StatusPublished},
			{SnapshotID: "fresh", ProviderID: "p", Namespace: "n", CreatedAt: now.Add(-10 * 24 * time.Hour), Status: snapshot.StatusPublished},
		},
		current: map[string]string{"p|n": "serving"},
	}
	revocations := &fakeRevocations{}
	report, err := newWorker(t, snapshots, revocations, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(report.ExpiredSnapshots) != 1 || report.ExpiredSnapshots[0] != "expired" {
		t.Fatalf("unexpected expired snapshots: %+v", report)
	}
	if len(snapshots.deleted) != 1 || snapshots.deleted[0] != "expired" {
		t.Fatalf("serving or fresh snapshots were deleted: %+v", snapshots.deleted)
	}
	if len(report.Failures) != 1 {
		t.Fatalf("the still-serving expired snapshot must be reported: %+v", report.Failures)
	}
}

func TestRetentionPurgesRevokedSubjectsAndCompletesStatus(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	revocations := &fakeRevocations{
		statuses: []revocation.Status{{
			RevocationID: "rev-1", OnlineStatus: revocation.OnlineDenied,
			PhysicalDeleteStatus: revocation.DeletePending, UpdatedAt: now.Add(-time.Hour),
		}},
		tokens: map[string][]revocation.Tombstone{
			"rev-1": {{Namespace: "n", Token: "tok-a"}, {Namespace: "n", Token: "tok-b"}},
		},
	}
	report, err := newWorker(t, &fakeSnapshots{}, revocations, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if report.PurgedSubjects != 2 || len(revocations.purged) != 2 {
		t.Fatalf("expected two purged subjects: %+v", report)
	}
	if len(revocations.appended) != 1 || revocations.appended[0].PhysicalDeleteStatus != revocation.DeleteCompleted {
		t.Fatalf("physical deletion was not completed: %+v", revocations.appended)
	}
}

func TestRetentionRespectsGraceAndReportsFailures(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	snapshots := &fakeSnapshots{
		jobs: []snapshot.Job{{SnapshotID: "expired", ProviderID: "p", Namespace: "n",
			CreatedAt: now.Add(-200 * 24 * time.Hour), Status: snapshot.StatusPublished}},
		current:   map[string]string{},
		deleteErr: errors.New("mutation failed"),
	}
	revocations := &fakeRevocations{
		statuses: []revocation.Status{{
			RevocationID: "rev-grace", PhysicalDeleteStatus: revocation.DeletePending,
			UpdatedAt: now.Add(-time.Minute), // inside the grace window
		}},
	}
	report, err := newWorker(t, snapshots, revocations, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(revocations.purged) != 0 {
		t.Fatalf("grace window must delay purges: %+v", revocations.purged)
	}
	if len(report.Failures) != 1 {
		t.Fatalf("deletion failure must be reported: %+v", report.Failures)
	}
}

func TestRetentionFlagsSLABreach(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	revocations := &fakeRevocations{
		statuses: []revocation.Status{{
			RevocationID: "rev-old", PhysicalDeleteStatus: revocation.DeletePending,
			UpdatedAt: now.Add(-31 * 24 * time.Hour),
		}},
		tokens: map[string][]revocation.Tombstone{"rev-old": {{Namespace: "n", Token: "tok"}}},
	}
	report, err := newWorker(t, &fakeSnapshots{}, revocations, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(report.Failures) == 0 {
		t.Fatal("SLA breach must be reported")
	}
}
