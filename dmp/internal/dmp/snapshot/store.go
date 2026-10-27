package snapshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/domain"
)

// Definitions is the control-plane view the snapshot store validates against.
type Definitions interface {
	GetProvider(ctx context.Context, id string) (control.Provider, error)
	GetNamespace(ctx context.Context, id string) (control.Namespace, error)
	ListSegments(ctx context.Context) ([]control.Segment, error)
}

// Store persists snapshot imports and membership rows for exactly one tenant.
type Store struct {
	conn   clickhouse.Conn
	defs   Definitions
	tenant uuid.UUID
	now    func() time.Time
}

// NewStore binds a ClickHouse connection and the definition reader to one tenant.
func NewStore(conn clickhouse.Conn, defs Definitions, tenantID uuid.UUID) *Store {
	return &Store{conn: conn, defs: defs, tenant: tenantID, now: func() time.Time { return time.Now().UTC() }}
}

// CreateJob registers an import for an active provider/namespace pair.
func (s *Store) CreateJob(ctx context.Context, input CreateJobInput) (Job, error) {
	if err := input.Validate(); err != nil {
		return Job{}, err
	}
	provider, err := s.defs.GetProvider(ctx, input.ProviderID)
	if err != nil || provider.Status != control.StatusActive {
		return Job{}, fmt.Errorf("%w: provider %q must exist and be active", ErrInvalidImport, input.ProviderID)
	}
	namespace, err := s.defs.GetNamespace(ctx, input.Namespace)
	if err != nil || namespace.Status != control.StatusActive {
		return Job{}, fmt.Errorf("%w: namespace %q must exist and be active", ErrInvalidImport, input.Namespace)
	}
	if provider.Namespace != input.Namespace {
		return Job{}, fmt.Errorf("%w: provider %q belongs to namespace %q", ErrInvalidImport, input.ProviderID, provider.Namespace)
	}

	raw, err := domain.NewUUIDv7()
	if err != nil {
		return Job{}, err
	}
	snapshotID, err := uuid.Parse(raw)
	if err != nil {
		return Job{}, err
	}
	job := Job{
		SnapshotID:       raw,
		ProviderID:       input.ProviderID,
		Namespace:        input.Namespace,
		SchemaVersion:    input.SchemaVersion,
		SourceVersion:    input.SourceVersion,
		ExpectedRows:     input.ExpectedRows,
		ExpectedChecksum: input.ExpectedChecksum,
		Status:           StatusImporting,
		CreatedAt:        s.now(),
		UpdatedAt:        s.now(),
		stateVersion:     1,
	}
	if err := s.insert(ctx, "dmp_snapshot_import", importColumns, s.importValues(snapshotID, job)); err != nil {
		return Job{}, err
	}
	return job, nil
}

// GetJob returns the latest state of one import.
func (s *Store) GetJob(ctx context.Context, snapshotID string) (Job, error) {
	id, err := uuid.Parse(snapshotID)
	if err != nil {
		return Job{}, ErrJobNotFound
	}
	rows, err := s.conn.Query(ctx, `SELECT snapshot_id, provider_id, namespace, schema_version, source_version,
			expected_rows, expected_checksum, status, failure_reason, state_version, created_at, updated_at
		FROM dmp_snapshot_import
		WHERE tenant_id = ? AND snapshot_id = ?
		ORDER BY state_version DESC
		LIMIT 1`, s.tenant, id)
	if err != nil {
		return Job{}, fmt.Errorf("%w: read import job: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Job{}, fmt.Errorf("%w: read import job: %v", ErrStoreUnavailable, err)
		}
		return Job{}, ErrJobNotFound
	}
	var job Job
	var readID uuid.UUID
	if err := rows.Scan(&readID, &job.ProviderID, &job.Namespace, &job.SchemaVersion, &job.SourceVersion,
		&job.ExpectedRows, &job.ExpectedChecksum, &job.Status, &job.FailureReason, &job.stateVersion,
		&job.CreatedAt, &job.UpdatedAt); err != nil {
		return Job{}, fmt.Errorf("%w: scan import job: %v", ErrStoreUnavailable, err)
	}
	job.SnapshotID = readID.String()
	rows.Close()
	if err := s.fillCounters(ctx, &job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// ListJobs returns every import for the tenant, newest first.
func (s *Store) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.conn.Query(ctx, `SELECT snapshot_id, provider_id, namespace, schema_version, source_version,
			expected_rows, expected_checksum, status, failure_reason, state_version, created_at, updated_at
		FROM dmp_snapshot_import
		WHERE tenant_id = ? AND (snapshot_id, state_version) IN (
			SELECT snapshot_id, max(state_version) FROM dmp_snapshot_import WHERE tenant_id = ? GROUP BY snapshot_id
		)
		ORDER BY created_at DESC`, s.tenant, s.tenant)
	if err != nil {
		return nil, fmt.Errorf("%w: list import jobs: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var job Job
		var readID uuid.UUID
		if err := rows.Scan(&readID, &job.ProviderID, &job.Namespace, &job.SchemaVersion, &job.SourceVersion,
			&job.ExpectedRows, &job.ExpectedChecksum, &job.Status, &job.FailureReason, &job.stateVersion,
			&job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan import job: %v", ErrStoreUnavailable, err)
		}
		job.SnapshotID = readID.String()
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list import jobs: %v", ErrStoreUnavailable, err)
	}
	for i := range jobs {
		if err := s.fillCounters(ctx, &jobs[i]); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

// AppendRows validates a trusted-adapter batch, skips rows that already exist
// (idempotent re-delivery) and stores the rest.
func (s *Store) AppendRows(ctx context.Context, snapshotID string, rows []MemberRow) (AppendResult, error) {
	if len(rows) == 0 {
		return AppendResult{}, fmt.Errorf("%w: rows must not be empty", ErrInvalidImport)
	}
	if len(rows) > MaxBatchRows {
		return AppendResult{}, fmt.Errorf("%w: at most %d rows per batch", ErrInvalidImport, MaxBatchRows)
	}
	job, err := s.GetJob(ctx, snapshotID)
	if err != nil {
		return AppendResult{}, err
	}
	if job.Status != StatusImporting {
		return AppendResult{}, fmt.Errorf("%w: job status is %s", ErrJobState, job.Status)
	}
	segments, err := s.providerSegments(ctx, job.ProviderID)
	if err != nil {
		return AppendResult{}, err
	}

	submitted := uint64(len(rows))
	seen := make(map[MemberKey]struct{}, len(rows)*2)
	unique := make([]MemberKey, 0, len(rows)*2)
	var duplicate uint64
	for i := range rows {
		if err := rows[i].Validate(); err != nil {
			return AppendResult{}, err
		}
		for _, segmentID := range rows[i].SegmentIDs {
			if _, ok := segments[segmentID]; !ok {
				return AppendResult{}, fmt.Errorf("%w: segment %q is not an active segment of provider %q", ErrInvalidImport, segmentID, job.ProviderID)
			}
			key := MemberKey{SubjectToken: rows[i].SubjectToken, SubjectKeyVersion: rows[i].SubjectKeyVersion, SegmentID: segmentID}
			if _, ok := seen[key]; ok {
				duplicate++
				continue
			}
			seen[key] = struct{}{}
			unique = append(unique, key)
		}
	}

	existing, err := s.existingKeys(ctx, snapshotID, unique)
	if err != nil {
		return AppendResult{}, err
	}
	fresh := make([]MemberKey, 0, len(unique))
	for _, key := range unique {
		if _, ok := existing[key]; ok {
			continue
		}
		fresh = append(fresh, key)
	}

	if len(fresh) > 0 {
		if err := s.insertMembers(ctx, job, fresh); err != nil {
			return AppendResult{}, err
		}
	}

	batchID, err := s.newUUID()
	if err != nil {
		return AppendResult{}, err
	}
	result := AppendResult{
		SubmittedRows:  submitted,
		StoredRows:     uint64(len(fresh)),
		IdempotentRows: uint64(len(unique) - len(fresh)),
		DuplicateRows:  duplicate,
	}
	if err := s.insert(ctx, "dmp_snapshot_import_batch",
		[]string{"tenant_id", "snapshot_id", "batch_id", "submitted_rows", "stored_rows", "idempotent_rows", "duplicate_rows", "received_at"},
		[]any{s.tenant, uuid.MustParse(job.SnapshotID), batchID, result.SubmittedRows, result.StoredRows, result.IdempotentRows, result.DuplicateRows, s.now()}); err != nil {
		return AppendResult{}, err
	}
	return result, nil
}

// Verify recomputes the stored rows and checksum and only promotes the job to
// verified when every expectation holds. A failed verification leaves the
// previously published snapshot untouched.
func (s *Store) Verify(ctx context.Context, snapshotID string) (Job, error) {
	job, err := s.GetJob(ctx, snapshotID)
	if err != nil {
		return Job{}, err
	}
	if job.Status == StatusVerified || job.Status == StatusPublished {
		return job, nil
	}
	if job.Status != StatusImporting {
		return job, fmt.Errorf("%w: job status is %s", ErrJobState, job.Status)
	}

	keys, err := s.Rows(ctx, job.SnapshotID)
	if err != nil {
		return Job{}, err
	}
	job.StoredRows = uint64(len(keys))
	checksum := Checksum(keys)

	var reason string
	switch {
	case job.StoredRows == 0:
		reason = "snapshot contains no membership rows"
	case job.ExpectedRows != job.StoredRows:
		reason = fmt.Sprintf("row count mismatch: expected %d, stored %d", job.ExpectedRows, job.StoredRows)
	case job.ExpectedChecksum != "" && job.ExpectedChecksum != checksum:
		reason = fmt.Sprintf("checksum mismatch: expected %s, stored %s", job.ExpectedChecksum, checksum)
	case job.DuplicateRate() > MaxDuplicateRate:
		reason = fmt.Sprintf("duplicate rate %.4f exceeds %.2f", job.DuplicateRate(), MaxDuplicateRate)
	}
	if reason != "" {
		failed, stateErr := s.appendState(ctx, job, StatusFailed, reason)
		if stateErr != nil {
			return Job{}, stateErr
		}
		return failed, fmt.Errorf("%w: %s", ErrVerification, reason)
	}
	return s.appendState(ctx, job, StatusVerified, "")
}

// Rows returns the distinct membership rows of one snapshot in canonical order.
func (s *Store) Rows(ctx context.Context, snapshotID string) ([]MemberKey, error) {
	id, err := uuid.Parse(snapshotID)
	if err != nil {
		return nil, ErrJobNotFound
	}
	rows, err := s.conn.Query(ctx, `SELECT subject_token, subject_token_key_version, segment_id
		FROM dmp_membership_snapshot
		WHERE tenant_id = ? AND snapshot_id = ?
		GROUP BY subject_token, subject_token_key_version, segment_id
		ORDER BY subject_token ASC, subject_token_key_version ASC, segment_id ASC`, s.tenant, id)
	if err != nil {
		return nil, fmt.Errorf("%w: read membership rows: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	var keys []MemberKey
	for rows.Next() {
		var key MemberKey
		if err := rows.Scan(&key.SubjectToken, &key.SubjectKeyVersion, &key.SegmentID); err != nil {
			return nil, fmt.Errorf("%w: scan membership row: %v", ErrStoreUnavailable, err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read membership rows: %v", ErrStoreUnavailable, err)
	}
	return keys, nil
}

// ChecksumOf returns the canonical digest of one snapshot's stored rows.
func (s *Store) ChecksumOf(ctx context.Context, snapshotID string) (uint64, string, error) {
	keys, err := s.Rows(ctx, snapshotID)
	if err != nil {
		return 0, "", err
	}
	return uint64(len(keys)), Checksum(keys), nil
}

func (s *Store) providerSegments(ctx context.Context, providerID string) (map[string]struct{}, error) {
	segments, err := s.defs.ListSegments(ctx)
	if err != nil {
		return nil, err
	}
	active := make(map[string]struct{}, len(segments))
	for _, segment := range segments {
		if segment.ProviderID == providerID && segment.Status == control.StatusActive {
			active[segment.ID] = struct{}{}
		}
	}
	return active, nil
}

func (s *Store) existingKeys(ctx context.Context, snapshotID string, keys []MemberKey) (map[MemberKey]struct{}, error) {
	existing := make(map[MemberKey]struct{})
	if len(keys) == 0 {
		return existing, nil
	}
	id, err := uuid.Parse(snapshotID)
	if err != nil {
		return nil, ErrJobNotFound
	}
	tokens := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key.SubjectToken]; ok {
			continue
		}
		seen[key.SubjectToken] = struct{}{}
		tokens = append(tokens, key.SubjectToken)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(tokens)), ",")
	args := make([]any, 0, len(tokens)+2)
	args = append(args, s.tenant, id)
	for _, token := range tokens {
		args = append(args, token)
	}
	rows, err := s.conn.Query(ctx, `SELECT subject_token, subject_token_key_version, segment_id
		FROM dmp_membership_snapshot
		WHERE tenant_id = ? AND snapshot_id = ? AND subject_token IN (`+placeholders+`)
		GROUP BY subject_token, subject_token_key_version, segment_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: read existing rows: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var key MemberKey
		if err := rows.Scan(&key.SubjectToken, &key.SubjectKeyVersion, &key.SegmentID); err != nil {
			return nil, fmt.Errorf("%w: scan existing row: %v", ErrStoreUnavailable, err)
		}
		existing[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read existing rows: %v", ErrStoreUnavailable, err)
	}
	return existing, nil
}

func (s *Store) insertMembers(ctx context.Context, job Job, keys []MemberKey) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO dmp_membership_snapshot
		(tenant_id, provider_id, namespace, snapshot_id, snapshot_month, subject_token,
		 subject_token_key_version, segment_id, ingested_at)`)
	if err != nil {
		return fmt.Errorf("%w: prepare membership insert: %v", ErrStoreUnavailable, err)
	}
	now := s.now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	snapshotID := uuid.MustParse(job.SnapshotID)
	for _, key := range keys {
		if err := batch.Append(s.tenant, job.ProviderID, job.Namespace, snapshotID, month,
			key.SubjectToken, key.SubjectKeyVersion, key.SegmentID, now); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("%w: append membership row: %v", ErrStoreUnavailable, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("%w: insert membership rows: %v", ErrStoreUnavailable, err)
	}
	return nil
}

func (s *Store) appendState(ctx context.Context, job Job, status, reason string) (Job, error) {
	snapshotID, err := uuid.Parse(job.SnapshotID)
	if err != nil {
		return Job{}, ErrJobNotFound
	}
	job.Status = status
	job.FailureReason = reason
	job.UpdatedAt = s.now()
	job.stateVersion++
	if err := s.insert(ctx, "dmp_snapshot_import", importColumns, s.importValues(snapshotID, job)); err != nil {
		return Job{}, err
	}
	return job, nil
}

var importColumns = []string{"tenant_id", "snapshot_id", "provider_id", "namespace", "schema_version", "source_version",
	"expected_rows", "expected_checksum", "submitted_rows", "duplicate_rows", "stored_rows", "status", "failure_reason",
	"state_version", "created_at", "updated_at"}

func (s *Store) importValues(snapshotID uuid.UUID, job Job) []any {
	return []any{s.tenant, snapshotID, job.ProviderID, job.Namespace, job.SchemaVersion, job.SourceVersion,
		job.ExpectedRows, job.ExpectedChecksum, job.SubmittedRows, job.DuplicateRows, job.StoredRows,
		job.Status, job.FailureReason, job.stateVersion, job.CreatedAt, job.UpdatedAt}
}

// fillCounters derives submission totals from the batch table and the current
// stored row count from the membership table.
func (s *Store) fillCounters(ctx context.Context, job *Job) error {
	snapshotID, err := uuid.Parse(job.SnapshotID)
	if err != nil {
		return ErrJobNotFound
	}
	rows, err := s.conn.Query(ctx, `SELECT sum(submitted_rows), sum(duplicate_rows)
		FROM dmp_snapshot_import_batch WHERE tenant_id = ? AND snapshot_id = ?`, s.tenant, snapshotID)
	if err != nil {
		return fmt.Errorf("%w: read batch totals: %v", ErrStoreUnavailable, err)
	}
	if rows.Next() {
		if err := rows.Scan(&job.SubmittedRows, &job.DuplicateRows); err != nil {
			rows.Close()
			return fmt.Errorf("%w: scan batch totals: %v", ErrStoreUnavailable, err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read batch totals: %v", ErrStoreUnavailable, err)
	}

	countRows, err := s.conn.Query(ctx, `SELECT count() FROM (
			SELECT subject_token, subject_token_key_version, segment_id
			FROM dmp_membership_snapshot
			WHERE tenant_id = ? AND snapshot_id = ?
			GROUP BY subject_token, subject_token_key_version, segment_id)`, s.tenant, snapshotID)
	if err != nil {
		return fmt.Errorf("%w: count stored rows: %v", ErrStoreUnavailable, err)
	}
	if countRows.Next() {
		if err := countRows.Scan(&job.StoredRows); err != nil {
			countRows.Close()
			return fmt.Errorf("%w: scan stored rows: %v", ErrStoreUnavailable, err)
		}
	}
	countRows.Close()
	if err := countRows.Err(); err != nil {
		return fmt.Errorf("%w: count stored rows: %v", ErrStoreUnavailable, err)
	}
	return nil
}

func (s *Store) newUUID() (uuid.UUID, error) {
	raw, err := domain.NewUUIDv7()
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("generate id: %w", err)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("parse id: %w", err)
	}
	return id, nil
}

func (s *Store) insert(ctx context.Context, table string, columns []string, values []any) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO "+table+" ("+strings.Join(columns, ", ")+")")
	if err != nil {
		return fmt.Errorf("%w: prepare %s insert: %v", ErrStoreUnavailable, table, err)
	}
	if err := batch.Append(values...); err != nil {
		_ = batch.Abort()
		return fmt.Errorf("%w: append %s row: %v", ErrStoreUnavailable, table, err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("%w: insert %s row: %v", ErrStoreUnavailable, table, err)
	}
	return nil
}
