package control

import (
	"context"
	"errors"
	"time"
)

// Mutation carries the caller identity recorded with every control-plane write.
type Mutation struct {
	Actor     string
	RequestID string
}

// AuditEntry is one append-only record of a control-plane attempt.
type AuditEntry struct {
	AuditID         string    `json:"audit_id"`
	Actor           string    `json:"actor"`
	Action          string    `json:"action"`
	ResourceType    string    `json:"resource_type"`
	ResourceID      string    `json:"resource_id"`
	ResourceVersion uint64    `json:"resource_version"`
	RequestID       string    `json:"request_id"`
	Outcome         string    `json:"outcome"`
	Detail          string    `json:"detail,omitempty"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// Audit actions and outcomes.
const (
	ActionCreate = "create"
	ActionUpdate = "update"

	OutcomeAccepted = "accepted"
	OutcomeConflict = "conflict"
	OutcomeRejected = "rejected"
	OutcomeNotFound = "not_found"
)

// ErrStoreUnavailable reports that the definition store could not serve the
// request. Callers must treat it as a temporary outage, not as "resource
// missing", and never fall back to unvalidated defaults.
var ErrStoreUnavailable = errors.New("definition store unavailable")

// AuditQuery filters the audit trail to one resource when set.
type AuditQuery struct {
	ResourceType string
	ResourceID   string
	Limit        int
}

// Store persists versioned definitions for exactly one tenant. Implementations
// must never accept a tenant from the caller, so a token cannot read or write
// another tenant's definitions.
type Store interface {
	ListNamespaces(ctx context.Context) ([]Namespace, error)
	GetNamespace(ctx context.Context, id string) (Namespace, error)
	CreateNamespace(ctx context.Context, input NamespaceInput, meta Mutation) (Namespace, error)
	UpdateNamespace(ctx context.Context, id string, input NamespaceInput, meta Mutation) (Namespace, error)

	ListProviders(ctx context.Context) ([]Provider, error)
	GetProvider(ctx context.Context, id string) (Provider, error)
	CreateProvider(ctx context.Context, input ProviderInput, meta Mutation) (Provider, error)
	UpdateProvider(ctx context.Context, id string, input ProviderInput, meta Mutation) (Provider, error)

	ListSegments(ctx context.Context) ([]Segment, error)
	GetSegment(ctx context.Context, id string) (Segment, error)
	CreateSegment(ctx context.Context, input SegmentInput, meta Mutation) (Segment, error)
	UpdateSegment(ctx context.Context, id string, input SegmentInput, meta Mutation) (Segment, error)

	ListAudit(ctx context.Context, query AuditQuery) ([]AuditEntry, error)
	// RecordRejected audits a request that was refused before any write, so a
	// rejected attempt is as traceable as an accepted one.
	RecordRejected(ctx context.Context, meta Mutation, action, resourceType, resourceID, detail string) error
}
