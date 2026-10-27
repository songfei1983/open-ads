// Package retention enforces the approved DMP retention policy: expired
// snapshots are deleted, revoked subjects are physically purged within the SLA
// and every sweep is reported so the policy is auditable.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/revocation"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
)

// Policy is the approved retention envelope.
type Policy struct {
	MembershipRetention time.Duration
	PhysicalDeleteGrace time.Duration
	PhysicalDeleteSLA   time.Duration
	EventRetention      time.Duration
}

// Snapshots is the store surface the worker needs.
type Snapshots interface {
	ListJobs(ctx context.Context) ([]snapshot.Job, error)
	CurrentPublication(ctx context.Context, providerID, namespace string) (snapshot.Publication, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) (uint64, error)
}

// Revocations is the tombstone surface the worker needs.
type Revocations interface {
	ListStatuses(ctx context.Context, limit int) ([]revocation.Status, error)
	TokensOf(ctx context.Context, revocationID string) ([]revocation.Tombstone, error)
	PurgeSubject(ctx context.Context, namespace, token string) (uint64, error)
	AppendStatus(ctx context.Context, status revocation.Status) (revocation.Status, error)
}

// Report summarises one sweep.
type Report struct {
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	ExpiredSnapshots []string  `json:"expired_snapshots"`
	DeletedRows      uint64    `json:"deleted_rows"`
	PurgedSubjects   int       `json:"purged_subjects"`
	PendingPurges    int       `json:"pending_purges"`
	Failures         []string  `json:"failures,omitempty"`
}

// Worker performs retention sweeps.
type Worker struct {
	snapshots   Snapshots
	revocations Revocations
	policy      Policy
	logger      *slog.Logger
	now         func() time.Time
}

// NewWorker builds a retention worker.
func NewWorker(snapshots Snapshots, revocations Revocations, policy Policy, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{snapshots: snapshots, revocations: revocations, policy: policy, logger: logger,
		now: func() time.Time { return time.Now().UTC() }}
}

// SetClockForTest replaces the clock in tests.
func (w *Worker) SetClockForTest(now func() time.Time) { w.now = now }

// Policy returns the effective policy (used by the status endpoint).
func (w *Worker) Policy() Policy { return w.policy }

// RunOnce deletes expired non-serving snapshots and completes due physical
// deletions. A failure on one item never blocks the rest of the sweep.
func (w *Worker) RunOnce(ctx context.Context) (Report, error) {
	report := Report{StartedAt: w.now(), ExpiredSnapshots: []string{}}

	jobs, err := w.snapshots.ListJobs(ctx)
	if err != nil {
		return report, fmt.Errorf("list snapshots: %w", err)
	}
	cutoff := report.StartedAt.Add(-w.policy.MembershipRetention)
	for _, job := range jobs {
		if job.CreatedAt.After(cutoff) || job.Status == snapshot.StatusRetired {
			continue
		}
		if current, pubErr := w.snapshots.CurrentPublication(ctx, job.ProviderID, job.Namespace); pubErr == nil && current.SnapshotID == job.SnapshotID {
			// The serving snapshot is never deleted; retention cannot silently
			// break resolution.
			report.Failures = append(report.Failures, "snapshot "+job.SnapshotID+" is past retention but still serving")
			continue
		} else if pubErr != nil && !errors.Is(pubErr, snapshot.ErrNoPublication) {
			report.Failures = append(report.Failures, "publication check "+job.SnapshotID+": "+pubErr.Error())
			continue
		}
		deleted, deleteErr := w.snapshots.DeleteSnapshot(ctx, job.SnapshotID)
		if deleteErr != nil {
			report.Failures = append(report.Failures, "delete "+job.SnapshotID+": "+deleteErr.Error())
			continue
		}
		report.ExpiredSnapshots = append(report.ExpiredSnapshots, job.SnapshotID)
		report.DeletedRows += deleted
	}

	statuses, err := w.revocations.ListStatuses(ctx, 200)
	if err != nil {
		return report, fmt.Errorf("list revocations: %w", err)
	}
	graceCutoff := report.StartedAt.Add(-w.policy.PhysicalDeleteGrace)
	slaCutoff := report.StartedAt.Add(-w.policy.PhysicalDeleteSLA)
	for _, status := range statuses {
		if status.PhysicalDeleteStatus != revocation.DeletePending {
			continue
		}
		if status.UpdatedAt.After(graceCutoff) {
			continue
		}
		if status.UpdatedAt.Before(slaCutoff) {
			report.Failures = append(report.Failures, "revocation "+status.RevocationID+" exceeded the physical-delete SLA")
		}
		tombstones, tokenErr := w.revocations.TokensOf(ctx, status.RevocationID)
		if tokenErr != nil {
			report.Failures = append(report.Failures, "tokens "+status.RevocationID+": "+tokenErr.Error())
			report.PendingPurges++
			continue
		}
		purged := 0
		for _, tombstone := range tombstones {
			if _, purgeErr := w.revocations.PurgeSubject(ctx, tombstone.Namespace, tombstone.Token); purgeErr != nil {
				report.Failures = append(report.Failures, "purge "+status.RevocationID+": "+purgeErr.Error())
				continue
			}
			purged++
		}
		if purged != len(tombstones) {
			report.PendingPurges++
			continue
		}
		status.PhysicalDeleteStatus = revocation.DeleteCompleted
		if _, appendErr := w.revocations.AppendStatus(ctx, status); appendErr != nil {
			report.Failures = append(report.Failures, "status "+status.RevocationID+": "+appendErr.Error())
			report.PendingPurges++
			continue
		}
		report.PurgedSubjects += purged
	}

	report.FinishedAt = w.now()
	w.logger.Info("retention sweep finished",
		slog.Int("expired_snapshots", len(report.ExpiredSnapshots)),
		slog.Uint64("deleted_rows", report.DeletedRows),
		slog.Int("purged_subjects", report.PurgedSubjects),
		slog.Int("pending_purges", report.PendingPurges),
		slog.Int("failures", len(report.Failures)))
	return report, nil
}

// Run loops until the context is cancelled.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			w.logger.Error("retention sweep failed", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
