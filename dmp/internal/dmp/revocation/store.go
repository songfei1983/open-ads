// Package revocation persists identity revocation tombstones and their
// append-only processing status in ClickHouse. ClickHouse is the authority; the
// Valkey deny gate is the online barrier derived from it.
package revocation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/songfei1983/open-ads/identity"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/domain"
)

// Store failures.
var (
	ErrInvalidRequest   = errors.New("invalid revocation request")
	ErrNotFound         = errors.New("revocation not found")
	ErrStoreUnavailable = errors.New("revocation store unavailable")
)

// Status values.
const (
	OnlinePending   = "pending"
	OnlineDenied    = "denied"
	DeletePending   = "pending"
	DeleteCompleted = "completed"
)

// TokenVersion is one subject token with the key version that produced it.
type TokenVersion struct {
	Token      string `json:"token"`
	KeyVersion uint16 `json:"key_version"`
}

// Request registers one revocation for a set of subject tokens.
type Request struct {
	RevocationID string         `json:"revocation_id,omitempty"`
	Namespace    string         `json:"namespace"`
	Tokens       []TokenVersion `json:"subject_tokens"`
	ReasonCode   string         `json:"reason_code,omitempty"`
	EffectiveAt  *time.Time     `json:"effective_at,omitempty"`
}

// Validate checks that the request only carries pseudonymous tokens.
func (r *Request) Validate() error {
	r.Namespace = strings.TrimSpace(r.Namespace)
	if r.Namespace == "" {
		return fmt.Errorf("%w: namespace is required", ErrInvalidRequest)
	}
	if len(r.Tokens) == 0 {
		return fmt.Errorf("%w: subject_tokens must not be empty", ErrInvalidRequest)
	}
	if len(r.Tokens) > 100 {
		return fmt.Errorf("%w: at most 100 subject tokens are allowed", ErrInvalidRequest)
	}
	seen := make(map[TokenVersion]struct{}, len(r.Tokens))
	for i := range r.Tokens {
		r.Tokens[i].Token = identity.Normalize(r.Tokens[i].Token)
		if err := identity.Default().Validate(r.Tokens[i].Token); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		if r.Tokens[i].KeyVersion == 0 {
			return fmt.Errorf("%w: key_version must be at least 1", ErrInvalidRequest)
		}
		if _, ok := seen[r.Tokens[i]]; ok {
			return fmt.Errorf("%w: subject tokens must be unique", ErrInvalidRequest)
		}
		seen[r.Tokens[i]] = struct{}{}
	}
	if r.RevocationID != "" {
		if _, err := uuid.Parse(r.RevocationID); err != nil {
			return fmt.Errorf("%w: revocation_id is invalid", ErrInvalidRequest)
		}
	}
	return nil
}

// Status is one append-only revocation processing state.
type Status struct {
	RevocationID         string    `json:"revocation_id"`
	Namespace            string    `json:"namespace,omitempty"`
	StatusVersion        uint64    `json:"status_version"`
	OnlineStatus         string    `json:"online_status"`
	PhysicalDeleteStatus string    `json:"physical_delete_status"`
	CoveredKeyVersions   []uint16  `json:"covered_key_versions"`
	GateEpoch            uint64    `json:"gate_epoch"`
	DrainedLookupCount   uint64    `json:"drained_lookup_count"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Tombstone is one persisted (namespace, token, key version) revocation fact.
type Tombstone struct {
	Namespace  string
	Token      string
	KeyVersion uint16
	ReasonCode string
}

// Store persists revocation facts for exactly one tenant.
type Store struct {
	conn   clickhouse.Conn
	tenant uuid.UUID
	now    func() time.Time
}

// NewStore binds a ClickHouse connection to one tenant.
func NewStore(conn clickhouse.Conn, tenantID uuid.UUID) *Store {
	return &Store{conn: conn, tenant: tenantID, now: func() time.Time { return time.Now().UTC() }}
}

// Record writes one tombstone per token/key-version pair. Repeating the same
// revocation is harmless: rows are append-only facts, not counters.
func (s *Store) Record(ctx context.Context, request Request) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO dmp_identity_revocation
		(tenant_id, namespace, subject_token, subject_token_key_version, revocation_id, effective_at, received_at, reason_code)`)
	if err != nil {
		return fmt.Errorf("%w: prepare tombstone insert: %v", ErrStoreUnavailable, err)
	}
	revocationID, err := uuid.Parse(request.RevocationID)
	if err != nil {
		return fmt.Errorf("%w: revocation_id is invalid", ErrInvalidRequest)
	}
	effectiveAt := s.now()
	if request.EffectiveAt != nil {
		effectiveAt = request.EffectiveAt.UTC()
	}
	receivedAt := s.now()
	for _, token := range request.Tokens {
		if err := batch.Append(s.tenant, request.Namespace, token.Token, token.KeyVersion, revocationID,
			effectiveAt, receivedAt, request.ReasonCode); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("%w: append tombstone: %v", ErrStoreUnavailable, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("%w: insert tombstones: %v", ErrStoreUnavailable, err)
	}
	return nil
}

// AppendStatus writes the next status version for a revocation.
func (s *Store) AppendStatus(ctx context.Context, status Status) (Status, error) {
	revocationID, err := uuid.Parse(status.RevocationID)
	if err != nil {
		return Status{}, ErrNotFound
	}
	latest, err := s.LatestStatus(ctx, status.RevocationID)
	switch {
	case err == nil:
		status.StatusVersion = latest.StatusVersion + 1
	case errors.Is(err, ErrNotFound):
		status.StatusVersion = 1
	default:
		return Status{}, err
	}
	status.UpdatedAt = s.now()
	if status.CoveredKeyVersions == nil {
		status.CoveredKeyVersions = []uint16{}
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO dmp_identity_revocation_status
		(tenant_id, revocation_id, status_version, online_status, physical_delete_status,
		 covered_key_versions, gate_epoch, drained_lookup_count, updated_at)`)
	if err != nil {
		return Status{}, fmt.Errorf("%w: prepare status insert: %v", ErrStoreUnavailable, err)
	}
	if err := batch.Append(s.tenant, revocationID, status.StatusVersion, status.OnlineStatus,
		status.PhysicalDeleteStatus, status.CoveredKeyVersions, status.GateEpoch,
		status.DrainedLookupCount, status.UpdatedAt); err != nil {
		_ = batch.Abort()
		return Status{}, fmt.Errorf("%w: append status: %v", ErrStoreUnavailable, err)
	}
	if err := batch.Send(); err != nil {
		return Status{}, fmt.Errorf("%w: insert status: %v", ErrStoreUnavailable, err)
	}
	return status, nil
}

// LatestStatus returns the newest status version of one revocation.
func (s *Store) LatestStatus(ctx context.Context, revocationID string) (Status, error) {
	id, err := uuid.Parse(revocationID)
	if err != nil {
		return Status{}, ErrNotFound
	}
	rows, err := s.conn.Query(ctx, `SELECT revocation_id, status_version, online_status, physical_delete_status,
			covered_key_versions, gate_epoch, drained_lookup_count, updated_at
		FROM dmp_identity_revocation_status
		WHERE tenant_id = ? AND revocation_id = ?
		ORDER BY status_version DESC
		LIMIT 1`, s.tenant, id)
	if err != nil {
		return Status{}, fmt.Errorf("%w: read status: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Status{}, fmt.Errorf("%w: read status: %v", ErrStoreUnavailable, err)
		}
		return Status{}, ErrNotFound
	}
	var status Status
	var readID uuid.UUID
	if err := rows.Scan(&readID, &status.StatusVersion, &status.OnlineStatus, &status.PhysicalDeleteStatus,
		&status.CoveredKeyVersions, &status.GateEpoch, &status.DrainedLookupCount, &status.UpdatedAt); err != nil {
		return Status{}, fmt.Errorf("%w: scan status: %v", ErrStoreUnavailable, err)
	}
	status.RevocationID = readID.String()
	return status, nil
}

// ListStatuses returns the newest status per revocation, newest first.
func (s *Store) ListStatuses(ctx context.Context, limit int) ([]Status, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.conn.Query(ctx, `SELECT revocation_id, status_version, online_status, physical_delete_status,
			covered_key_versions, gate_epoch, drained_lookup_count, updated_at
		FROM dmp_identity_revocation_status
		WHERE tenant_id = ? AND (revocation_id, status_version) IN (
			SELECT revocation_id, max(status_version) FROM dmp_identity_revocation_status
			WHERE tenant_id = ? GROUP BY revocation_id)
		ORDER BY updated_at DESC
		LIMIT ?`, s.tenant, s.tenant, uint64(limit))
	if err != nil {
		return nil, fmt.Errorf("%w: list statuses: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	var statuses []Status
	for rows.Next() {
		var status Status
		var readID uuid.UUID
		if err := rows.Scan(&readID, &status.StatusVersion, &status.OnlineStatus, &status.PhysicalDeleteStatus,
			&status.CoveredKeyVersions, &status.GateEpoch, &status.DrainedLookupCount, &status.UpdatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan status: %v", ErrStoreUnavailable, err)
		}
		status.RevocationID = readID.String()
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list statuses: %v", ErrStoreUnavailable, err)
	}
	return statuses, nil
}

// Tombstones returns every distinct revocation fact of the tenant. The deny gate
// uses it to rebuild after a Valkey keyspace loss.
func (s *Store) Tombstones(ctx context.Context) ([]Tombstone, error) {
	rows, err := s.conn.Query(ctx, `SELECT namespace, subject_token, subject_token_key_version, any(reason_code)
		FROM dmp_identity_revocation
		WHERE tenant_id = ?
		GROUP BY namespace, subject_token, subject_token_key_version
		ORDER BY namespace ASC, subject_token ASC`, s.tenant)
	if err != nil {
		return nil, fmt.Errorf("%w: read tombstones: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	var tombstones []Tombstone
	for rows.Next() {
		var tombstone Tombstone
		if err := rows.Scan(&tombstone.Namespace, &tombstone.Token, &tombstone.KeyVersion, &tombstone.ReasonCode); err != nil {
			return nil, fmt.Errorf("%w: scan tombstone: %v", ErrStoreUnavailable, err)
		}
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read tombstones: %v", ErrStoreUnavailable, err)
	}
	return tombstones, nil
}

// NewID returns a canonical UUID for a new revocation.
func NewID() (string, error) {
	raw, err := domain.NewUUIDv7()
	if err != nil {
		return "", fmt.Errorf("generate revocation id: %w", err)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse revocation id: %w", err)
	}
	return id.String(), nil
}

// TokensOf returns the tombstones recorded for one revocation.
func (s *Store) TokensOf(ctx context.Context, revocationID string) ([]Tombstone, error) {
	id, err := uuid.Parse(revocationID)
	if err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.conn.Query(ctx, `SELECT namespace, subject_token, subject_token_key_version, any(reason_code)
		FROM dmp_identity_revocation
		WHERE tenant_id = ? AND revocation_id = ?
		GROUP BY namespace, subject_token, subject_token_key_version`, s.tenant, id)
	if err != nil {
		return nil, fmt.Errorf("%w: read revocation tokens: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	var tombstones []Tombstone
	for rows.Next() {
		var tombstone Tombstone
		if err := rows.Scan(&tombstone.Namespace, &tombstone.Token, &tombstone.KeyVersion, &tombstone.ReasonCode); err != nil {
			return nil, fmt.Errorf("%w: scan revocation token: %v", ErrStoreUnavailable, err)
		}
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read revocation tokens: %v", ErrStoreUnavailable, err)
	}
	return tombstones, nil
}

// TokenCounts returns how many membership and event rows still reference the
// subject. Physical deletion is only complete at zero.
func (s *Store) TokenCounts(ctx context.Context, namespace, token string) (uint64, error) {
	var membership, events uint64
	if err := s.count(ctx, `SELECT count() FROM dmp_membership_snapshot
		WHERE tenant_id = ? AND namespace = ? AND subject_token = ?`, namespace, token, &membership); err != nil {
		return 0, err
	}
	if err := s.count(ctx, `SELECT count() FROM dmp_event
		WHERE tenant_id = ? AND namespace = ? AND subject_token = ?`, namespace, token, &events); err != nil {
		return 0, err
	}
	return membership + events, nil
}

// PurgeSubject physically deletes membership and analytic event rows for one
// revoked subject and records how many rows each snapshot lost, so the serving
// snapshot's counts stay explainable. The tombstone is kept: it is the
// authoritative deny record and the proof that the deletion was requested.
func (s *Store) PurgeSubject(ctx context.Context, namespace, token string) (uint64, error) {
	perSnapshot, err := s.membershipCounts(ctx, namespace, token)
	if err != nil {
		return 0, err
	}
	for _, statement := range []string{
		`ALTER TABLE dmp_membership_snapshot DELETE WHERE tenant_id = ? AND namespace = ? AND subject_token = ?
		 SETTINGS mutations_sync = 1`,
		`ALTER TABLE dmp_event DELETE WHERE tenant_id = ? AND namespace = ? AND subject_token = ?
		 SETTINGS mutations_sync = 1`,
	} {
		if err := s.conn.Exec(ctx, statement, s.tenant, namespace, token); err != nil {
			return 0, fmt.Errorf("%w: purge subject rows: %v", ErrStoreUnavailable, err)
		}
	}
	remaining, err := s.TokenCounts(ctx, namespace, token)
	if err != nil {
		return 0, err
	}
	if remaining != 0 {
		return 0, fmt.Errorf("%w: %d rows survived the purge", ErrStoreUnavailable, remaining)
	}
	if len(perSnapshot) > 0 {
		if err := s.recordPurge(ctx, namespace, token, perSnapshot); err != nil {
			return 0, err
		}
	}
	var purged uint64
	for _, count := range perSnapshot {
		purged += count
	}
	return purged, nil
}

// membershipCounts reports how many membership rows each snapshot holds for a
// subject before the purge runs.
func (s *Store) membershipCounts(ctx context.Context, namespace, token string) (map[uuid.UUID]uint64, error) {
	rows, err := s.conn.Query(ctx, `SELECT snapshot_id, count()
		FROM dmp_membership_snapshot
		WHERE tenant_id = ? AND namespace = ? AND subject_token = ?
		GROUP BY snapshot_id`, s.tenant, namespace, token)
	if err != nil {
		return nil, fmt.Errorf("%w: count subject memberships: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	counts := make(map[uuid.UUID]uint64)
	for rows.Next() {
		var snapshotID uuid.UUID
		var count uint64
		if err := rows.Scan(&snapshotID, &count); err != nil {
			return nil, fmt.Errorf("%w: scan subject membership count: %v", ErrStoreUnavailable, err)
		}
		counts[snapshotID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: count subject memberships: %v", ErrStoreUnavailable, err)
	}
	return counts, nil
}

func (s *Store) recordPurge(ctx context.Context, namespace, token string, perSnapshot map[uuid.UUID]uint64) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO dmp_snapshot_purge
		(tenant_id, snapshot_id, namespace, subject_token, purged_rows, reason_code, purged_at)`)
	if err != nil {
		return fmt.Errorf("%w: prepare purge record: %v", ErrStoreUnavailable, err)
	}
	now := s.now()
	for snapshotID, count := range perSnapshot {
		if err := batch.Append(s.tenant, snapshotID, namespace, token, count, "revocation_purge", now); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("%w: append purge record: %v", ErrStoreUnavailable, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("%w: insert purge records: %v", ErrStoreUnavailable, err)
	}
	return nil
}

func (s *Store) count(ctx context.Context, query, namespace, token string, destination *uint64) error {
	rows, err := s.conn.Query(ctx, query, s.tenant, namespace, token)
	if err != nil {
		return fmt.Errorf("%w: count subject rows: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(destination); err != nil {
			return fmt.Errorf("%w: scan subject row count: %v", ErrStoreUnavailable, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: count subject rows: %v", ErrStoreUnavailable, err)
	}
	return nil
}
