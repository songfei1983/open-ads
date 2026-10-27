package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SegmentRef is one resolved segment; names and profile details never leave DMP.
type SegmentRef struct {
	ID string `json:"id"`
}

// Publication pins the snapshot currently serving one provider/namespace.
type Publication struct {
	ProviderID   string    `json:"provider_id"`
	Namespace    string    `json:"namespace"`
	SnapshotID   string    `json:"snapshot_id"`
	ExpectedRows uint64    `json:"expected_rows"`
	Checksum     string    `json:"checksum"`
	CreatedAt    time.Time `json:"created_at"`
	PublishedAt  time.Time `json:"published_at"`
}

// ResolveInput is one audience lookup.
type ResolveInput struct {
	RequestID      string   `json:"request_id"`
	TenantID       string   `json:"tenant_id,omitempty"`
	ProviderID     string   `json:"provider_id,omitempty"`
	Namespace      string   `json:"namespace"`
	SubjectToken   string   `json:"subject_token"`
	SnapshotID     string   `json:"snapshot_id,omitempty"`
	Purposes       []string `json:"purposes,omitempty"`
	ConsentContext any      `json:"consent_context,omitempty"`
	MaxSegments    int      `json:"max_segments,omitempty"`
}

// ResolveResult is the phone-free answer: segment identifiers only.
type ResolveResult struct {
	RequestID  string       `json:"request_id"`
	SnapshotID string       `json:"snapshot_id"`
	ProviderID string       `json:"provider_id"`
	Segments   []SegmentRef `json:"segments"`
	ResolvedAt time.Time    `json:"resolved_at"`
}

// MaxResolveSegments bounds any single lookup.
const MaxResolveSegments = 100

// DefaultResolveSegments is used when the caller omits max_segments.
const DefaultResolveSegments = 10

// Validate checks the lookup contract before any storage access.
func (in *ResolveInput) Validate() error {
	in.Namespace = strings.TrimSpace(in.Namespace)
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.SubjectToken = strings.ToLower(strings.TrimSpace(in.SubjectToken))
	if in.Namespace == "" {
		return fmt.Errorf("%w: namespace is required", ErrInvalidImport)
	}
	if !subjectTokenPattern.MatchString(in.SubjectToken) {
		return fmt.Errorf("%w: subject_token must be a 64-character lowercase hex digest", ErrInvalidImport)
	}
	if in.MaxSegments == 0 {
		in.MaxSegments = DefaultResolveSegments
	}
	if in.MaxSegments < 0 || in.MaxSegments > MaxResolveSegments {
		return fmt.Errorf("%w: max_segments must be between 1 and %d", ErrInvalidImport, MaxResolveSegments)
	}
	if in.SnapshotID != "" {
		if _, err := uuid.Parse(in.SnapshotID); err != nil {
			return fmt.Errorf("%w: snapshot_id is invalid", ErrInvalidImport)
		}
	}
	return nil
}

// Publish promotes a verified snapshot to the serving pointer for its
// provider/namespace. The stored rows are re-verified first, so a corrupted
// membership table can never be published.
func (s *Store) Publish(ctx context.Context, snapshotID string) (Job, error) {
	job, err := s.GetJob(ctx, snapshotID)
	if err != nil {
		return Job{}, err
	}
	if job.Status == StatusPublished {
		return job, nil
	}
	// A previous attempt may have written the publication marker and then lost
	// its state append (ClickHouse has no transactions). Reconcile instead of
	// publishing twice.
	if _, pubErr := s.PublicationOf(ctx, job.SnapshotID); pubErr == nil {
		if job.Status == StatusVerified {
			return s.appendState(ctx, job, StatusPublished, "")
		}
		return job, nil
	} else if !errors.Is(pubErr, ErrNoPublication) {
		return Job{}, pubErr
	}
	if job.Status != StatusVerified {
		return job, fmt.Errorf("%w: job status is %s", ErrNotVerified, job.Status)
	}
	count, checksum, err := s.ChecksumOf(ctx, snapshotID)
	if err != nil {
		return Job{}, err
	}
	if count == 0 || count != job.ExpectedRows {
		return Job{}, fmt.Errorf("%w: stored rows %d do not match expected %d", ErrVerification, count, job.ExpectedRows)
	}
	if job.ExpectedChecksum != "" && checksum != job.ExpectedChecksum {
		return Job{}, fmt.Errorf("%w: stored checksum does not match the verified checksum", ErrVerification)
	}

	id, err := uuid.Parse(job.SnapshotID)
	if err != nil {
		return Job{}, ErrJobNotFound
	}
	now := s.now()
	if err := s.insert(ctx, "dmp_snapshot_publication",
		[]string{"tenant_id", "provider_id", "namespace", "snapshot_id", "created_at", "published_at", "expected_rows", "checksum"},
		[]any{s.tenant, job.ProviderID, job.Namespace, id, job.CreatedAt, now, count, checksum}); err != nil {
		return Job{}, err
	}
	return s.appendState(ctx, job, StatusPublished, "")
}

// CurrentPublication returns the snapshot currently serving a provider/namespace.
func (s *Store) CurrentPublication(ctx context.Context, providerID, namespace string) (Publication, error) {
	rows, err := s.conn.Query(ctx, `SELECT provider_id, namespace, snapshot_id, expected_rows, checksum, created_at, published_at
		FROM dmp_snapshot_publication
		WHERE tenant_id = ? AND provider_id = ? AND namespace = ?
		ORDER BY published_at DESC, snapshot_id DESC
		LIMIT 1`, s.tenant, providerID, namespace)
	if err != nil {
		return Publication{}, fmt.Errorf("%w: read publication: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Publication{}, fmt.Errorf("%w: read publication: %v", ErrStoreUnavailable, err)
		}
		return Publication{}, ErrNoPublication
	}
	return scanPublication(rows)
}

// PublicationOf returns the publication marker of one specific snapshot.
func (s *Store) PublicationOf(ctx context.Context, snapshotID string) (Publication, error) {
	id, err := uuid.Parse(snapshotID)
	if err != nil {
		return Publication{}, ErrJobNotFound
	}
	rows, err := s.conn.Query(ctx, `SELECT provider_id, namespace, snapshot_id, expected_rows, checksum, created_at, published_at
		FROM dmp_snapshot_publication
		WHERE tenant_id = ? AND snapshot_id = ?
		ORDER BY published_at DESC
		LIMIT 1`, s.tenant, id)
	if err != nil {
		return Publication{}, fmt.Errorf("%w: read publication: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Publication{}, fmt.Errorf("%w: read publication: %v", ErrStoreUnavailable, err)
		}
		return Publication{}, ErrNoPublication
	}
	return scanPublication(rows)
}

// Resolve looks up segments for one pseudonymous subject. By default it uses
// the current publication; an explicit snapshot_id is only honoured when that
// snapshot was published, so unpublished imports are never resolvable.
func (s *Store) Resolve(ctx context.Context, in ResolveInput) (ResolveResult, error) {
	if err := in.Validate(); err != nil {
		return ResolveResult{}, err
	}
	var publication Publication
	var err error
	if in.SnapshotID != "" {
		publication, err = s.PublicationOf(ctx, in.SnapshotID)
		if err == nil && in.ProviderID != "" && publication.ProviderID != in.ProviderID {
			return ResolveResult{}, ErrNoPublication
		}
		if err == nil && publication.Namespace != in.Namespace {
			return ResolveResult{}, ErrNoPublication
		}
	} else if in.ProviderID != "" {
		publication, err = s.CurrentPublication(ctx, in.ProviderID, in.Namespace)
	} else {
		err = ErrNoPublication
	}
	if err != nil {
		if errors.Is(err, ErrNoPublication) {
			return ResolveResult{}, ErrNoPublication
		}
		return ResolveResult{}, err
	}

	snapshotID, err := uuid.Parse(publication.SnapshotID)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("%w: publication snapshot id is invalid", ErrStoreUnavailable)
	}
	limit := in.MaxSegments
	rows, err := s.conn.Query(ctx, `SELECT DISTINCT segment_id
		FROM dmp_membership_snapshot
		WHERE tenant_id = ? AND provider_id = ? AND namespace = ? AND snapshot_id = ? AND subject_token = ?
		ORDER BY segment_id ASC
		LIMIT ?`, s.tenant, publication.ProviderID, publication.Namespace, snapshotID, in.SubjectToken, uint64(limit))
	if err != nil {
		return ResolveResult{}, fmt.Errorf("%w: read segments: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	segments := []SegmentRef{}
	for rows.Next() {
		var segmentID string
		if err := rows.Scan(&segmentID); err != nil {
			return ResolveResult{}, fmt.Errorf("%w: scan segment: %v", ErrStoreUnavailable, err)
		}
		segments = append(segments, SegmentRef{ID: segmentID})
	}
	if err := rows.Err(); err != nil {
		return ResolveResult{}, fmt.Errorf("%w: read segments: %v", ErrStoreUnavailable, err)
	}
	return ResolveResult{
		RequestID:  in.RequestID,
		SnapshotID: publication.SnapshotID,
		ProviderID: publication.ProviderID,
		Segments:   segments,
		ResolvedAt: s.now(),
	}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPublication(row rowScanner) (Publication, error) {
	var publication Publication
	var readID uuid.UUID
	if err := row.Scan(&publication.ProviderID, &publication.Namespace, &readID, &publication.ExpectedRows,
		&publication.Checksum, &publication.CreatedAt, &publication.PublishedAt); err != nil {
		return Publication{}, fmt.Errorf("%w: scan publication: %v", ErrStoreUnavailable, err)
	}
	publication.SnapshotID = readID.String()
	return publication, nil
}

// ErrPublished reports a snapshot that still serves traffic and therefore cannot
// be deleted.
var ErrPublished = errors.New("snapshot is the current publication")

// DeleteSnapshot physically removes one snapshot's membership rows through a
// controlled mutation keyed by snapshot_id. Because the filter is tenant+snapshot
// scoped, other snapshots sharing the same monthly partition are untouched.
func (s *Store) DeleteSnapshot(ctx context.Context, snapshotID string) (uint64, error) {
	job, err := s.GetJob(ctx, snapshotID)
	if err != nil {
		return 0, err
	}
	if job.Status == StatusPublished {
		return 0, ErrPublished
	}
	if current, err := s.CurrentPublication(ctx, job.ProviderID, job.Namespace); err == nil && current.SnapshotID == job.SnapshotID {
		return 0, ErrPublished
	} else if err != nil && !errors.Is(err, ErrNoPublication) {
		return 0, err
	}

	before, _, err := s.ChecksumOf(ctx, job.SnapshotID)
	if err != nil {
		return 0, err
	}
	id, err := uuid.Parse(job.SnapshotID)
	if err != nil {
		return 0, ErrJobNotFound
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE dmp_membership_snapshot DELETE
		WHERE tenant_id = ? AND snapshot_id = ?
		SETTINGS mutations_sync = 1`, s.tenant, id); err != nil {
		return 0, fmt.Errorf("%w: delete snapshot rows: %v", ErrStoreUnavailable, err)
	}
	after, _, err := s.ChecksumOf(ctx, job.SnapshotID)
	if err != nil {
		return 0, err
	}
	if after != 0 {
		return before, fmt.Errorf("%w: %d rows survived the deletion", ErrStoreUnavailable, after)
	}
	if job.Status != StatusRetired {
		if _, err := s.appendState(ctx, job, StatusRetired, "retention deletion"); err != nil {
			return before, err
		}
	}
	return before, nil
}

// CreatedBefore reports whether the snapshot import started before cutoff.
func (s *Store) CreatedBefore(ctx context.Context, snapshotID string, cutoff time.Time) (bool, error) {
	job, err := s.GetJob(ctx, snapshotID)
	if err != nil {
		return false, err
	}
	return job.CreatedAt.Before(cutoff), nil
}

// ListCurrentPublications returns the snapshot serving each provider/namespace.
func (s *Store) ListCurrentPublications(ctx context.Context) ([]Publication, error) {
	rows, err := s.conn.Query(ctx, `SELECT provider_id, namespace, snapshot_id, expected_rows, checksum, created_at, published_at
		FROM dmp_snapshot_publication
		WHERE tenant_id = ? AND (provider_id, namespace, published_at) IN (
			SELECT provider_id, namespace, max(published_at) FROM dmp_snapshot_publication
			WHERE tenant_id = ? GROUP BY provider_id, namespace)
		ORDER BY published_at DESC`, s.tenant, s.tenant)
	if err != nil {
		return nil, fmt.Errorf("%w: list publications: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	var publications []Publication
	for rows.Next() {
		publication, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		publications = append(publications, publication)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list publications: %v", ErrStoreUnavailable, err)
	}
	return publications, nil
}
