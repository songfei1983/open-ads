// Package revocation persists identity revocation tombstones and their
// append-only processing status in ClickHouse. ClickHouse is the authority; the
// Valkey deny gate is the online barrier derived from it.
package revocation

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		r.Tokens[i].Token = strings.ToLower(strings.TrimSpace(r.Tokens[i].Token))
		if !isToken(r.Tokens[i].Token) {
			return fmt.Errorf("%w: subject_token must be a 64-character lowercase hex digest", ErrInvalidRequest)
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

func isToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
