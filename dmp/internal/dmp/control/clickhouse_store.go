package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/domain"
)

var (
	namespaceColumns = []string{"tenant_id", "namespace", "version", "revision_id", "display_name",
		"hashing_algorithm", "subject_key_version", "retention_months", "status", "written_at"}
	providerColumns = []string{"tenant_id", "provider_id", "version", "revision_id", "display_name",
		"namespace", "license_version", "status", "written_at"}
	segmentColumns = []string{"tenant_id", "provider_id", "segment_id", "version", "revision_id", "name",
		"description", "status", "valid_from", "written_at"}
	auditColumns = []string{"tenant_id", "audit_id", "occurred_at", "actor", "action", "resource_type",
		"resource_id", "resource_version", "request_id", "outcome", "detail"}
)

// ClickHouseStore stores definitions as append-only versions. ClickHouse has no
// multi-statement transactions, so an optimistic update inserts the next version
// and then re-reads it: the winner is the row with the lowest revision id, and a
// concurrent writer of the same version is reported as a conflict instead of
// silently overwriting.
type ClickHouseStore struct {
	conn     clickhouse.Conn
	tenantID uuid.UUID
	now      func() time.Time
}

// NewClickHouseStore binds a connection to exactly one tenant.
func NewClickHouseStore(conn clickhouse.Conn, tenantID uuid.UUID) *ClickHouseStore {
	return &ClickHouseStore{conn: conn, tenantID: tenantID, now: func() time.Time { return time.Now().UTC() }}
}

// OpenClickHouse opens a connection pool and verifies it is reachable.
func OpenClickHouse(ctx context.Context, addr, user, password, database string) (clickhouse.Conn, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:         []string{addr},
		Auth:         clickhouse.Auth{Database: database, Username: user, Password: password},
		DialTimeout:  5 * time.Second,
		MaxOpenConns: 4,
		MaxIdleConns: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	return conn, nil
}

// Close releases the underlying connection pool.
func (s *ClickHouseStore) Close() error {
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// ListNamespaces returns the current version of every namespace.
func (s *ClickHouseStore) ListNamespaces(ctx context.Context) ([]Namespace, error) {
	return s.listNamespaces(ctx, "")
}

// GetNamespace returns the current version of one namespace.
func (s *ClickHouseStore) GetNamespace(ctx context.Context, id string) (Namespace, error) {
	items, err := s.listNamespaces(ctx, id)
	if err != nil {
		return Namespace{}, err
	}
	if len(items) == 0 {
		return Namespace{}, ErrNotFound
	}
	return items[0], nil
}

func (s *ClickHouseStore) listNamespaces(ctx context.Context, id string) ([]Namespace, error) {
	query := `SELECT namespace, version, revision_id, display_name, hashing_algorithm,
		subject_key_version, retention_months, status, written_at
		FROM dmp_namespace_version
		WHERE tenant_id = ?`
	args := []any{s.tenantID}
	if id != "" {
		query += ` AND namespace = ?`
		args = append(args, id)
	}
	query += ` ORDER BY namespace ASC, version DESC, revision_id ASC`

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list namespaces: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()

	var items []Namespace
	for rows.Next() {
		var item Namespace
		if err := rows.Scan(&item.ID, &item.Version, &item.revisionID, &item.DisplayName,
			&item.HashingAlgorithm, &item.SubjectKeyVersion, &item.RetentionMonths,
			&item.Status, &item.WrittenAt); err != nil {
			return nil, fmt.Errorf("%w: scan namespace: %v", ErrStoreUnavailable, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list namespaces: %v", ErrStoreUnavailable, err)
	}
	current := make([]Namespace, 0, len(items))
	for _, item := range items {
		if len(current) > 0 && current[len(current)-1].ID == item.ID {
			continue
		}
		current = append(current, item)
	}
	return current, nil
}

// CreateNamespace writes version 1 of a namespace definition.
func (s *ClickHouseStore) CreateNamespace(ctx context.Context, input NamespaceInput, meta Mutation) (Namespace, error) {
	if err := input.Validate(); err != nil {
		return Namespace{}, err
	}
	if _, err := s.GetNamespace(ctx, input.ID); err == nil {
		return Namespace{}, s.auditFailure(ctx, meta, ActionCreate, "namespace", input.ID, 0, OutcomeConflict, "definition already exists", ErrAlreadyExists)
	} else if !errors.Is(err, ErrNotFound) {
		return Namespace{}, err
	}

	revision, writtenAt, err := s.newRevision()
	if err != nil {
		return Namespace{}, err
	}
	if err := s.insert(ctx, "dmp_namespace_version", namespaceColumns,
		[]any{s.tenantID, input.ID, uint64(1), revision, input.DisplayName, input.HashingAlgorithm, input.SubjectKeyVersion, input.RetentionMonths, input.Status, writtenAt}); err != nil {
		return Namespace{}, err
	}

	created, err := s.GetNamespace(ctx, input.ID)
	if err != nil {
		return Namespace{}, err
	}
	if created.Version != 1 || created.revisionID != revision {
		return Namespace{}, s.auditFailure(ctx, meta, ActionCreate, "namespace", input.ID, created.Version, OutcomeConflict, "concurrent create", ErrVersionConflict)
	}
	if err := s.recordAudit(ctx, meta, ActionCreate, "namespace", input.ID, created.Version, OutcomeAccepted, ""); err != nil {
		return Namespace{}, err
	}
	return created, nil
}

// UpdateNamespace appends the next version when input.Version is current.
func (s *ClickHouseStore) UpdateNamespace(ctx context.Context, id string, input NamespaceInput, meta Mutation) (Namespace, error) {
	input.ID = id
	if err := input.Validate(); err != nil {
		return Namespace{}, err
	}
	current, err := s.GetNamespace(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Namespace{}, s.auditFailure(ctx, meta, ActionUpdate, "namespace", id, 0, OutcomeNotFound, "definition not found", err)
		}
		return Namespace{}, err
	}
	if current.Version != input.Version {
		return Namespace{}, s.auditFailure(ctx, meta, ActionUpdate, "namespace", id, input.Version, OutcomeConflict,
			fmt.Sprintf("stored version %d", current.Version), ErrVersionConflict)
	}

	next := current.Version + 1
	revision, writtenAt, err := s.newRevision()
	if err != nil {
		return Namespace{}, err
	}
	if err := s.insert(ctx, "dmp_namespace_version", namespaceColumns,
		[]any{s.tenantID, id, next, revision, input.DisplayName, input.HashingAlgorithm, input.SubjectKeyVersion, input.RetentionMonths, input.Status, writtenAt}); err != nil {
		return Namespace{}, err
	}

	updated, err := s.GetNamespace(ctx, id)
	if err != nil {
		return Namespace{}, err
	}
	if updated.Version != next || updated.revisionID != revision {
		return Namespace{}, s.auditFailure(ctx, meta, ActionUpdate, "namespace", id, next, OutcomeConflict, "concurrent version write", ErrVersionConflict)
	}
	if err := s.recordAudit(ctx, meta, ActionUpdate, "namespace", id, updated.Version, OutcomeAccepted, ""); err != nil {
		return Namespace{}, err
	}
	return updated, nil
}

// ListProviders returns the current version of every provider.
func (s *ClickHouseStore) ListProviders(ctx context.Context) ([]Provider, error) {
	return s.listProviders(ctx, "")
}

// GetProvider returns the current version of one provider.
func (s *ClickHouseStore) GetProvider(ctx context.Context, id string) (Provider, error) {
	items, err := s.listProviders(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if len(items) == 0 {
		return Provider{}, ErrNotFound
	}
	return items[0], nil
}

func (s *ClickHouseStore) listProviders(ctx context.Context, id string) ([]Provider, error) {
	query := `SELECT provider_id, version, revision_id, display_name, namespace, license_version, status, written_at
		FROM dmp_provider_version
		WHERE tenant_id = ?`
	args := []any{s.tenantID}
	if id != "" {
		query += ` AND provider_id = ?`
		args = append(args, id)
	}
	query += ` ORDER BY provider_id ASC, version DESC, revision_id ASC`

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list providers: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()

	var items []Provider
	for rows.Next() {
		var item Provider
		if err := rows.Scan(&item.ID, &item.Version, &item.revisionID, &item.DisplayName,
			&item.Namespace, &item.LicenseVersion, &item.Status, &item.WrittenAt); err != nil {
			return nil, fmt.Errorf("%w: scan provider: %v", ErrStoreUnavailable, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list providers: %v", ErrStoreUnavailable, err)
	}
	current := make([]Provider, 0, len(items))
	for _, item := range items {
		if len(current) > 0 && current[len(current)-1].ID == item.ID {
			continue
		}
		current = append(current, item)
	}
	return current, nil
}

// CreateProvider writes version 1 of a provider definition.
func (s *ClickHouseStore) CreateProvider(ctx context.Context, input ProviderInput, meta Mutation) (Provider, error) {
	if err := input.Validate(); err != nil {
		return Provider{}, err
	}
	if _, err := s.GetProvider(ctx, input.ID); err == nil {
		return Provider{}, s.auditFailure(ctx, meta, ActionCreate, "provider", input.ID, 0, OutcomeConflict, "definition already exists", ErrAlreadyExists)
	} else if !errors.Is(err, ErrNotFound) {
		return Provider{}, err
	}
	namespace, err := s.GetNamespace(ctx, input.Namespace)
	if err != nil || namespace.Status != StatusActive {
		return Provider{}, s.auditFailure(ctx, meta, ActionCreate, "provider", input.ID, 0, OutcomeRejected,
			"namespace is missing or not active", fmt.Errorf("%w: namespace %q must exist and be active", ErrInvalidDefinition, input.Namespace))
	}

	revision, writtenAt, err := s.newRevision()
	if err != nil {
		return Provider{}, err
	}
	if err := s.insert(ctx, "dmp_provider_version", providerColumns,
		[]any{s.tenantID, input.ID, uint64(1), revision, input.DisplayName, input.Namespace, input.LicenseVersion, input.Status, writtenAt}); err != nil {
		return Provider{}, err
	}

	created, err := s.GetProvider(ctx, input.ID)
	if err != nil {
		return Provider{}, err
	}
	if created.Version != 1 || created.revisionID != revision {
		return Provider{}, s.auditFailure(ctx, meta, ActionCreate, "provider", input.ID, created.Version, OutcomeConflict, "concurrent create", ErrVersionConflict)
	}
	if err := s.recordAudit(ctx, meta, ActionCreate, "provider", input.ID, created.Version, OutcomeAccepted, ""); err != nil {
		return Provider{}, err
	}
	return created, nil
}

// UpdateProvider appends the next version when input.Version is current.
func (s *ClickHouseStore) UpdateProvider(ctx context.Context, id string, input ProviderInput, meta Mutation) (Provider, error) {
	input.ID = id
	if err := input.Validate(); err != nil {
		return Provider{}, err
	}
	current, err := s.GetProvider(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Provider{}, s.auditFailure(ctx, meta, ActionUpdate, "provider", id, 0, OutcomeNotFound, "definition not found", err)
		}
		return Provider{}, err
	}
	if current.Version != input.Version {
		return Provider{}, s.auditFailure(ctx, meta, ActionUpdate, "provider", id, input.Version, OutcomeConflict,
			fmt.Sprintf("stored version %d", current.Version), ErrVersionConflict)
	}
	if current.Namespace != input.Namespace {
		return Provider{}, s.auditFailure(ctx, meta, ActionUpdate, "provider", id, input.Version, OutcomeRejected,
			"namespace change rejected", fmt.Errorf("%w: namespace cannot change after creation", ErrImmutableField))
	}

	next := current.Version + 1
	revision, writtenAt, err := s.newRevision()
	if err != nil {
		return Provider{}, err
	}
	if err := s.insert(ctx, "dmp_provider_version", providerColumns,
		[]any{s.tenantID, id, next, revision, input.DisplayName, current.Namespace, input.LicenseVersion, input.Status, writtenAt}); err != nil {
		return Provider{}, err
	}

	updated, err := s.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if updated.Version != next || updated.revisionID != revision {
		return Provider{}, s.auditFailure(ctx, meta, ActionUpdate, "provider", id, next, OutcomeConflict, "concurrent version write", ErrVersionConflict)
	}
	if err := s.recordAudit(ctx, meta, ActionUpdate, "provider", id, updated.Version, OutcomeAccepted, ""); err != nil {
		return Provider{}, err
	}
	return updated, nil
}

// ListSegments returns the current version of every segment.
func (s *ClickHouseStore) ListSegments(ctx context.Context) ([]Segment, error) {
	return s.listSegments(ctx, "")
}

// GetSegment returns the current version of one segment.
func (s *ClickHouseStore) GetSegment(ctx context.Context, id string) (Segment, error) {
	items, err := s.listSegments(ctx, id)
	if err != nil {
		return Segment{}, err
	}
	if len(items) == 0 {
		return Segment{}, ErrNotFound
	}
	return items[0], nil
}

func (s *ClickHouseStore) listSegments(ctx context.Context, id string) ([]Segment, error) {
	query := `SELECT segment_id, version, revision_id, provider_id, name, description, status, valid_from, written_at
		FROM dmp_segment_definition_version
		WHERE tenant_id = ?`
	args := []any{s.tenantID}
	if id != "" {
		query += ` AND segment_id = ?`
		args = append(args, id)
	}
	query += ` ORDER BY segment_id ASC, version DESC, revision_id ASC`

	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list segments: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()

	var items []Segment
	for rows.Next() {
		var item Segment
		if err := rows.Scan(&item.ID, &item.Version, &item.revisionID, &item.ProviderID, &item.Name,
			&item.Description, &item.Status, &item.ValidFrom, &item.WrittenAt); err != nil {
			return nil, fmt.Errorf("%w: scan segment: %v", ErrStoreUnavailable, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list segments: %v", ErrStoreUnavailable, err)
	}
	current := make([]Segment, 0, len(items))
	for _, item := range items {
		if len(current) > 0 && current[len(current)-1].ID == item.ID {
			continue
		}
		current = append(current, item)
	}
	return current, nil
}

// CreateSegment writes version 1 of a segment definition.
func (s *ClickHouseStore) CreateSegment(ctx context.Context, input SegmentInput, meta Mutation) (Segment, error) {
	if err := input.Validate(); err != nil {
		return Segment{}, err
	}
	if _, err := s.GetSegment(ctx, input.ID); err == nil {
		return Segment{}, s.auditFailure(ctx, meta, ActionCreate, "segment", input.ID, 0, OutcomeConflict, "definition already exists", ErrAlreadyExists)
	} else if !errors.Is(err, ErrNotFound) {
		return Segment{}, err
	}
	provider, err := s.GetProvider(ctx, input.ProviderID)
	if err != nil || provider.Status != StatusActive {
		return Segment{}, s.auditFailure(ctx, meta, ActionCreate, "segment", input.ID, 0, OutcomeRejected,
			"provider is missing or not active", fmt.Errorf("%w: provider %q must exist and be active", ErrInvalidDefinition, input.ProviderID))
	}

	revision, writtenAt, err := s.newRevision()
	if err != nil {
		return Segment{}, err
	}
	if err := s.insert(ctx, "dmp_segment_definition_version", segmentColumns,
		[]any{s.tenantID, input.ProviderID, input.ID, uint64(1), revision, input.Name, input.Description, input.Status, *input.ValidFrom, writtenAt}); err != nil {
		return Segment{}, err
	}

	created, err := s.GetSegment(ctx, input.ID)
	if err != nil {
		return Segment{}, err
	}
	if created.Version != 1 || created.revisionID != revision {
		return Segment{}, s.auditFailure(ctx, meta, ActionCreate, "segment", input.ID, created.Version, OutcomeConflict, "concurrent create", ErrVersionConflict)
	}
	if err := s.recordAudit(ctx, meta, ActionCreate, "segment", input.ID, created.Version, OutcomeAccepted, ""); err != nil {
		return Segment{}, err
	}
	return created, nil
}

// UpdateSegment appends the next version when input.Version is current.
func (s *ClickHouseStore) UpdateSegment(ctx context.Context, id string, input SegmentInput, meta Mutation) (Segment, error) {
	input.ID = id
	if err := input.Validate(); err != nil {
		return Segment{}, err
	}
	current, err := s.GetSegment(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Segment{}, s.auditFailure(ctx, meta, ActionUpdate, "segment", id, 0, OutcomeNotFound, "definition not found", err)
		}
		return Segment{}, err
	}
	if current.Version != input.Version {
		return Segment{}, s.auditFailure(ctx, meta, ActionUpdate, "segment", id, input.Version, OutcomeConflict,
			fmt.Sprintf("stored version %d", current.Version), ErrVersionConflict)
	}
	if current.ProviderID != input.ProviderID {
		return Segment{}, s.auditFailure(ctx, meta, ActionUpdate, "segment", id, input.Version, OutcomeRejected,
			"provider change rejected", fmt.Errorf("%w: provider_id cannot change after creation", ErrImmutableField))
	}

	next := current.Version + 1
	revision, writtenAt, err := s.newRevision()
	if err != nil {
		return Segment{}, err
	}
	if err := s.insert(ctx, "dmp_segment_definition_version", segmentColumns,
		[]any{s.tenantID, current.ProviderID, id, next, revision, input.Name, input.Description, input.Status, *input.ValidFrom, writtenAt}); err != nil {
		return Segment{}, err
	}

	updated, err := s.GetSegment(ctx, id)
	if err != nil {
		return Segment{}, err
	}
	if updated.Version != next || updated.revisionID != revision {
		return Segment{}, s.auditFailure(ctx, meta, ActionUpdate, "segment", id, next, OutcomeConflict, "concurrent version write", ErrVersionConflict)
	}
	if err := s.recordAudit(ctx, meta, ActionUpdate, "segment", id, updated.Version, OutcomeAccepted, ""); err != nil {
		return Segment{}, err
	}
	return updated, nil
}

// ListAudit returns the newest audit entries first.
func (s *ClickHouseStore) ListAudit(ctx context.Context, query AuditQuery) ([]AuditEntry, error) {
	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sql := `SELECT audit_id, actor, action, resource_type, resource_id, resource_version,
		request_id, outcome, detail, occurred_at
		FROM dmp_control_audit
		WHERE tenant_id = ?`
	args := []any{s.tenantID}
	if query.ResourceType != "" {
		sql += ` AND resource_type = ?`
		args = append(args, query.ResourceType)
	}
	if query.ResourceID != "" {
		sql += ` AND resource_id = ?`
		args = append(args, query.ResourceID)
	}
	sql += ` ORDER BY occurred_at DESC, audit_id DESC LIMIT ?`
	args = append(args, uint64(limit))

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list audit: %v", ErrStoreUnavailable, err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(&entry.AuditID, &entry.Actor, &entry.Action, &entry.ResourceType,
			&entry.ResourceID, &entry.ResourceVersion, &entry.RequestID, &entry.Outcome,
			&entry.Detail, &entry.OccurredAt); err != nil {
			return nil, fmt.Errorf("%w: scan audit: %v", ErrStoreUnavailable, err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list audit: %v", ErrStoreUnavailable, err)
	}
	return entries, nil
}

// RecordRejected audits a definition rejected before persistence.
func (s *ClickHouseStore) RecordRejected(ctx context.Context, meta Mutation, action, resourceType, resourceID, detail string) error {
	return s.recordAudit(ctx, meta, action, resourceType, resourceID, 0, OutcomeRejected, detail)
}

func (s *ClickHouseStore) insert(ctx context.Context, table string, columns []string, values []any) error {
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

func (s *ClickHouseStore) newRevision() (uuid.UUID, time.Time, error) {
	raw, err := domain.NewUUIDv7()
	if err != nil {
		return uuid.UUID{}, time.Time{}, fmt.Errorf("generate revision id: %w", err)
	}
	revision, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, time.Time{}, fmt.Errorf("parse revision id: %w", err)
	}
	return revision, s.now(), nil
}

func (s *ClickHouseStore) auditFailure(ctx context.Context, meta Mutation, action, resourceType, resourceID string, version uint64, outcome, detail string, cause error) error {
	if err := s.recordAudit(ctx, meta, action, resourceType, resourceID, version, outcome, detail); err != nil {
		// The audit trail is part of the contract: an attempt that cannot be
		// recorded must not be reported with a logical outcome.
		return fmt.Errorf("record audit for %s %q: %w", resourceType, resourceID, err)
	}
	return cause
}

func (s *ClickHouseStore) recordAudit(ctx context.Context, meta Mutation, action, resourceType, resourceID string, version uint64, outcome, detail string) error {
	raw, err := domain.NewUUIDv7()
	if err != nil {
		return fmt.Errorf("generate audit id: %w", err)
	}
	auditID, err := uuid.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse audit id: %w", err)
	}
	actor := meta.Actor
	if actor == "" {
		actor = "unknown"
	}
	return s.insert(ctx, "dmp_control_audit", auditColumns,
		[]any{s.tenantID, auditID, s.now(), actor, action, resourceType, resourceID, version, meta.RequestID, outcome, detail})
}
