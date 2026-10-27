// Package internalapi serves the DMP internal API used by trusted adapters:
// snapshot import/verification (and, in later iterations, publication,
// audience resolution and revocation).
package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/authn"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/gate"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/retention"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/revocation"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/snapshot"
)

// SnapshotService is the trusted-adapter view of the snapshot lifecycle.
type SnapshotService interface {
	CreateJob(ctx context.Context, input snapshot.CreateJobInput) (snapshot.Job, error)
	ListJobs(ctx context.Context) ([]snapshot.Job, error)
	GetJob(ctx context.Context, snapshotID string) (snapshot.Job, error)
	AppendRows(ctx context.Context, snapshotID string, rows []snapshot.MemberRow) (snapshot.AppendResult, error)
	Verify(ctx context.Context, snapshotID string) (snapshot.Job, error)
	Publish(ctx context.Context, snapshotID string) (snapshot.Job, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) (uint64, error)
	ListCurrentPublications(ctx context.Context) ([]snapshot.Publication, error)
	Resolve(ctx context.Context, input snapshot.ResolveInput) (snapshot.ResolveResult, error)
}

// DenyGate is the online revocation barrier the resolver admits against.
type DenyGate interface {
	Admit(ctx context.Context, namespace, token, leaseID string) error
	Finalize(ctx context.Context, namespace, token, leaseID string) error
	Deny(ctx context.Context, namespace, token, reason string) (uint64, error)
	InFlight(ctx context.Context, namespace, token string) (uint64, error)
	Epoch(ctx context.Context) (uint64, error)
}

// Revocations persists tombstones and their processing status.
type Revocations interface {
	Record(ctx context.Context, request revocation.Request) error
	AppendStatus(ctx context.Context, status revocation.Status) (revocation.Status, error)
	LatestStatus(ctx context.Context, revocationID string) (revocation.Status, error)
	ListStatuses(ctx context.Context, limit int) ([]revocation.Status, error)
}

// RetentionService runs and reports the retention policy.
type RetentionService interface {
	RunOnce(ctx context.Context) (retention.Report, error)
	Policy() retention.Policy
}

// HandlerOptions carries the trusted-adapter dependency set.
type HandlerOptions struct {
	// Authenticator identifies trusted callers. Token/AdminToken build the
	// default sandbox credential set when it is nil.
	Authenticator authn.Authenticator
	Token         string
	AdminToken    string
	// TenantID is the single tenant this data plane serves; every authenticated
	// principal must match it and resolve requests are bound to it.
	TenantID    string
	Snapshots   SnapshotService
	Gate        DenyGate
	Revocations Revocations
	Retention   RetentionService
	// Metrics records the business signals; nil disables them.
	Metrics *observability.DMPMetrics
	Logger  *slog.Logger
}

// NewHandler returns the internal API. Every request must present the internal
// bearer token: this surface is only for trusted tokenization/import adapters,
// never for browsers or anonymous callers.
func NewHandler(opts HandlerOptions) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	authenticator := opts.Authenticator
	if authenticator == nil {
		authenticator = sandboxAuthenticator(opts.Token, opts.AdminToken)
	}
	h := &handler{authenticator: authenticator, tenantID: opts.TenantID, snapshots: opts.Snapshots, gate: opts.Gate,
		revocations: opts.Revocations, retention: opts.Retention, logger: logger, metrics: opts.Metrics,
		configured: opts.Authenticator != nil || opts.Token != "" || opts.AdminToken != ""}
	return h.authenticate(h)
}

// sandboxAuthenticator keeps the shared-token profile working while scopes stay
// explicit; D-01 replaces it with mTLS workload identities.
func sandboxAuthenticator(token, adminToken string) authn.Authenticator {
	all := []authn.Scope{authn.ScopeSnapshotsRead, authn.ScopeSnapshotsWrite, authn.ScopeIdentityRevoke, authn.ScopeRetentionRun}
	return authn.NewBearerAuth(
		authn.Credential{Token: token, Caller: "trusted-adapter", Scopes: all},
		authn.Credential{Token: adminToken, Caller: "operator", Scopes: all},
	)
}

type handler struct {
	authenticator authn.Authenticator
	tenantID      string
	configured    bool
	snapshots     SnapshotService
	gate          DenyGate
	revocations   Revocations
	retention     RetentionService
	metrics       *observability.DMPMetrics
	logger        *slog.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/v1"), "/")
	parts := strings.Split(path, "/")
	switch {
	case path == "snapshots":
		switch r.Method {
		case http.MethodGet:
			if h.requireScope(w, r, authn.ScopeSnapshotsRead) {
				h.listSnapshots(w, r)
			}
		case http.MethodPost:
			if h.requireScope(w, r, authn.ScopeSnapshotsWrite) {
				h.createSnapshot(w, r)
			}
		default:
			methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
		}
	case len(parts) == 2 && parts[0] == "snapshots":
		switch {
		case r.Method == http.MethodGet:
			if h.requireScope(w, r, authn.ScopeSnapshotsRead) {
				h.getSnapshot(w, r, parts[1])
			}
		default:
			methodNotAllowed(w, r, http.MethodGet)
		}
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "rows":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsWrite) {
			h.appendRows(w, r, parts[1])
		}
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "verify":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsWrite) {
			h.verifySnapshot(w, r, parts[1])
		}
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsWrite) {
			h.deleteSnapshot(w, r, parts[1])
		}
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "publish":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsWrite) {
			h.publishSnapshot(w, r, parts[1])
		}
	case path == "publications":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, http.MethodGet)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsRead) {
			h.listPublications(w, r)
		}
	case path == "audiences:resolve":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		h.resolveAudience(w, r)
	case path == "identity:revoke":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		if h.requireScope(w, r, authn.ScopeIdentityRevoke) {
			h.revokeIdentity(w, r)
		}
	case len(parts) == 2 && parts[0] == "identity-revocations":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, http.MethodGet)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsRead) {
			h.getRevocation(w, r, parts[1])
		}
	case path == "identity-revocations":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, http.MethodGet)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsRead) {
			h.listRevocations(w, r)
		}
	case path == "retention:run":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		if h.requireScope(w, r, authn.ScopeRetentionRun) {
			h.runRetention(w, r)
		}
	case path == "retention:policy":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, http.MethodGet)
			return
		}
		if h.requireScope(w, r, authn.ScopeSnapshotsRead) {
			h.retentionPolicy(w, r)
		}
	default:
		notFound(w, r)
	}
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := observability.RequestIDFromContext(r.Context())
		if h.snapshots == nil || !h.configured {
			writeError(w, requestID, http.StatusServiceUnavailable, "internal_api_unconfigured", "DMP internal API is not configured")
			return
		}
		if isResolvePath(r.URL.Path) {
			// Sandbox profile: SSP calls resolve without a workload identity, so
			// the request is bound to this data plane's tenant instead. D-01
			// replaces this with mTLS caller authorization.
			next.ServeHTTP(w, r)
			return
		}
		principal, err := h.authenticatePrincipal(r)
		if err != nil {
			writeError(w, requestID, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		if h.tenantID != "" && principal.Tenant != "" && principal.Tenant != h.tenantID {
			writeError(w, requestID, http.StatusForbidden, "tenant_mismatch", "credential is not bound to this tenant")
			return
		}
		next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), principal)))
	})
}

func (h *handler) authenticatePrincipal(r *http.Request) (authn.Principal, error) {
	if h.authenticator == nil {
		return authn.Principal{}, fmt.Errorf("%w: DMP internal API is not configured", authn.ErrUnauthenticated)
	}
	return h.authenticator.Authenticate(r)
}

// requireScope enforces the route scope for authenticated callers.
func (h *handler) requireScope(w http.ResponseWriter, r *http.Request, scope authn.Scope) bool {
	principal, ok := authn.PrincipalFromContext(r.Context())
	if !ok {
		return true
	}
	if err := principal.Authorize(scope); err != nil {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusForbidden, "insufficient_scope",
			fmt.Sprintf("caller %q lacks scope %s", principal.Caller, scope))
		return false
	}
	return true
}

func isResolvePath(path string) bool {
	return strings.Trim(strings.TrimPrefix(path, "/internal/v1"), "/") == "audiences:resolve"
}

func (h *handler) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var input snapshot.CreateJobInput
	if !decode(w, r, &input) {
		return
	}
	job, err := h.snapshots.CreateJob(r.Context(), input)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (h *handler) listSnapshots(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.snapshots.ListJobs(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if jobs == nil {
		jobs = []snapshot.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *handler) getSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	job, err := h.snapshots.GetJob(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *handler) appendRows(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Rows []snapshot.MemberRow `json:"rows"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := h.snapshots.AppendRows(r.Context(), id, input.Rows)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.metrics != nil {
		h.metrics.Import(result.StoredRows)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) verifySnapshot(w http.ResponseWriter, r *http.Request, id string) {
	job, err := h.snapshots.Verify(r.Context(), id)
	if err != nil {
		if errors.Is(err, snapshot.ErrVerification) {
			h.observeVerify("failed")
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"code":       "verification_failed",
				"message":    err.Error(),
				"request_id": observability.RequestIDFromContext(r.Context()),
				"job":        job,
			})
			return
		}
		if errors.Is(err, snapshot.ErrSealed) {
			h.observeVerify("sealed")
		}
		h.fail(w, r, err)
		return
	}
	h.observeVerifySuccess()
	writeJSON(w, http.StatusOK, job)
}

func (h *handler) publishSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	job, err := h.snapshots.Publish(r.Context(), id)
	if err != nil {
		if errors.Is(err, snapshot.ErrVerification) {
			h.observePublish("verification_failed")
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusUnprocessableEntity, "verification_failed", err.Error())
			return
		}
		h.observePublish("error")
		h.fail(w, r, err)
		return
	}
	h.observePublish("published")
	writeJSON(w, http.StatusOK, job)
}

func (h *handler) listPublications(w http.ResponseWriter, r *http.Request) {
	publications, err := h.snapshots.ListCurrentPublications(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if publications == nil {
		publications = []snapshot.Publication{}
	}
	writeJSON(w, http.StatusOK, publications)
}

func (h *handler) deleteSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	deleted, err := h.snapshots.DeleteSnapshot(r.Context(), id)
	if err != nil {
		if errors.Is(err, snapshot.ErrPublished) {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusConflict, "invalid_state", "the serving snapshot cannot be deleted")
			return
		}
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot_id": id, "deleted_rows": deleted})
}

func (h *handler) resolveAudience(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var input snapshot.ResolveInput
	if !decode(w, r, &input) {
		return
	}
	input.RequestID = observability.RequestIDFromContext(r.Context())
	if err := input.Validate(); err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.bindResolveTenant(w, r, &input) {
		return
	}

	leaseID, err := revocation.NewID()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.gate != nil {
		switch err := h.gate.Admit(r.Context(), input.Namespace, input.SubjectToken, leaseID); {
		case err == nil:
			h.observeGate("admitted")
		case errors.Is(err, gate.ErrSubjectDenied):
			// A revoked subject never reaches storage: answer with no audience.
			h.observeGate("denied")
			h.observeResolve("denied", 0, started)
			writeJSON(w, http.StatusOK, snapshot.ResolveResult{
				RequestID: input.RequestID, ProviderID: input.ProviderID,
				Segments: []snapshot.SegmentRef{}, ResolvedAt: time.Now().UTC(),
			})
			return
		default:
			h.observeGate("unavailable")
			h.observeResolve("unavailable", 0, started)
			h.gateUnavailable(w, r, err)
			return
		}
	}

	result, resolveErr := h.snapshots.Resolve(r.Context(), input)

	if h.gate != nil {
		finalizeErr := h.gate.Finalize(r.Context(), input.Namespace, input.SubjectToken, leaseID)
		if resolveErr == nil && finalizeErr != nil {
			// The answer may only be released if the barrier still holds.
			if errors.Is(finalizeErr, gate.ErrSubjectDenied) {
				h.observeGate("finalize_denied")
				result.Segments = []snapshot.SegmentRef{}
			} else {
				h.observeGate("finalize_unavailable")
				h.observeResolve("unavailable", 0, started)
				h.gateUnavailable(w, r, finalizeErr)
				return
			}
		}
	}

	if resolveErr != nil {
		if errors.Is(resolveErr, snapshot.ErrNoPublication) {
			h.observeResolve("no_publication", 0, started)
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "no_published_snapshot", "no published snapshot serves this provider/namespace")
			return
		}
		h.observeResolve("error", 0, started)
		h.fail(w, r, resolveErr)
		return
	}
	if len(result.Segments) == 0 {
		h.observeResolve("empty", 0, started)
	} else {
		h.observeResolve("resolved", len(result.Segments), started)
	}
	writeJSON(w, http.StatusOK, result)
}

// legacyTenantAlias is the symbolic tenant the sandbox SSP sends. It resolves to
// this data plane's tenant until D-01 binds tenants to workload identities.
const legacyTenantAlias = "tenant-default"

// bindResolveTenant requires an explicit tenant and refuses cross-tenant
// lookups, so a caller can never read another tenant's audience.
func (h *handler) bindResolveTenant(w http.ResponseWriter, r *http.Request, input *snapshot.ResolveInput) bool {
	if h.tenantID == "" {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "internal_api_unconfigured", "no tenant is bound to this data plane")
		return false
	}
	tenant := strings.TrimSpace(input.TenantID)
	if tenant == "" {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "tenant_required", "tenant_id is required for audience resolution")
		return false
	}
	if tenant == legacyTenantAlias {
		tenant = h.tenantID
	}
	if tenant != h.tenantID {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusForbidden, "tenant_mismatch", "tenant_id does not match the tenant bound to this data plane")
		return false
	}
	input.TenantID = tenant
	return true
}

func (h *handler) observeResolve(result string, segments int, started time.Time) {
	if h.metrics == nil {
		return
	}
	h.metrics.Resolve(result, segments, time.Since(started))
}

func (h *handler) observeVerify(result string) {
	if h.metrics == nil {
		return
	}
	h.metrics.Verify(result)
}

// observeVerifySuccess records a verification that matched the manifest.
func (h *handler) observeVerifySuccess() { h.observeVerify("verified") }

func (h *handler) observeRetention(result string, report retention.Report) {
	if h.metrics == nil {
		return
	}
	h.metrics.Retention(result, report.DeletedRows, report.PurgedSubjects, report.PendingPurges)
}

func (h *handler) observePublish(result string) {
	if h.metrics == nil {
		return
	}
	h.metrics.Publish(result)
}

func (h *handler) observeGate(result string) {
	if h.metrics == nil {
		return
	}
	h.metrics.GateAdmission(result)
}

func (h *handler) gateUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("DMP deny gate unavailable", slog.String("request_id", observability.RequestIDFromContext(r.Context())), slog.Any("error", err))
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "policy_state_unavailable", "deny gate state is unavailable; refusing to return an audience")
}

func (h *handler) revokeIdentity(w http.ResponseWriter, r *http.Request) {
	if h.gate == nil || h.revocations == nil {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "internal_api_unconfigured", "DMP deny gate is not configured")
		return
	}
	var request revocation.Request
	if !decode(w, r, &request) {
		return
	}
	if err := request.Validate(); err != nil {
		h.fail(w, r, err)
		return
	}
	if request.RevocationID == "" {
		id, err := revocation.NewID()
		if err != nil {
			h.fail(w, r, err)
			return
		}
		request.RevocationID = id
	}
	request.ReasonCode = gate.NormalizeReason(request.ReasonCode)

	epoch, err := h.gate.Epoch(r.Context())
	if err != nil {
		h.gateUnavailable(w, r, err)
		return
	}

	if h.metrics != nil {
		h.metrics.Revocation(len(request.Tokens))
	}
	var registered uint64
	for _, token := range request.Tokens {
		count, denyErr := h.gate.Deny(r.Context(), request.Namespace, token.Token, request.ReasonCode)
		if denyErr != nil {
			// Some tokens may already be denied; that is safe. Nothing is
			// answered as completed while the barrier is uncertain.
			h.gateUnavailable(w, r, denyErr)
			return
		}
		registered += count
	}

	tombstoneErr := h.revocations.Record(r.Context(), request)
	remaining, drainErr := h.waitForDrain(r.Context(), request)
	onlineStatus := revocation.OnlineDenied
	if tombstoneErr != nil || drainErr != nil || remaining > 0 {
		onlineStatus = revocation.OnlinePending
	}

	status, statusErr := h.revocations.AppendStatus(r.Context(), revocation.Status{
		RevocationID:         request.RevocationID,
		StatusVersion:        0,
		OnlineStatus:         onlineStatus,
		PhysicalDeleteStatus: revocation.DeletePending,
		CoveredKeyVersions:   coveredKeyVersions(request),
		GateEpoch:            epoch,
		DrainedLookupCount:   registered,
	})
	response := map[string]any{
		"revocation_id":          request.RevocationID,
		"status":                 "pending",
		"online_status":          onlineStatus,
		"physical_delete_status": revocation.DeletePending,
		"gate_epoch":             epoch,
		"drained_lookup_count":   registered,
	}
	if onlineStatus == revocation.OnlineDenied {
		response["status"] = "completed"
	}
	if statusErr != nil {
		response["warning"] = "revocation status could not be persisted"
		h.logger.Error("DMP revocation status could not be persisted",
			slog.String("request_id", observability.RequestIDFromContext(r.Context())), slog.Any("error", statusErr))
	} else {
		response["status_version"] = status.StatusVersion
	}
	if tombstoneErr != nil {
		response["warning"] = "revocation tombstone is pending; the online deny gate is already active"
		h.logger.Error("DMP revocation tombstone could not be persisted",
			slog.String("request_id", observability.RequestIDFromContext(r.Context())), slog.Any("error", tombstoneErr))
	}
	writeJSON(w, http.StatusAccepted, response)
}

// waitForDrain waits briefly for in-flight lookups to finish after the deny is
// set. It never blocks a revocation for longer than the bounded window.
func (h *handler) waitForDrain(ctx context.Context, request revocation.Request) (uint64, error) {
	deadline := time.Now().Add(1 * time.Second)
	var remaining uint64
	for {
		remaining = 0
		for _, token := range request.Tokens {
			count, err := h.gate.InFlight(ctx, request.Namespace, token.Token)
			if err != nil {
				return 0, err
			}
			remaining += count
		}
		if remaining == 0 || time.Now().After(deadline) {
			return remaining, nil
		}
		select {
		case <-ctx.Done():
			return remaining, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func coveredKeyVersions(request revocation.Request) []uint16 {
	seen := make(map[uint16]struct{}, len(request.Tokens))
	versions := make([]uint16, 0, len(request.Tokens))
	for _, token := range request.Tokens {
		if _, ok := seen[token.KeyVersion]; ok {
			continue
		}
		seen[token.KeyVersion] = struct{}{}
		versions = append(versions, token.KeyVersion)
	}
	return versions
}

func (h *handler) getRevocation(w http.ResponseWriter, r *http.Request, id string) {
	if h.revocations == nil {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "internal_api_unconfigured", "revocation store is not configured")
		return
	}
	status, err := h.revocations.LatestStatus(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *handler) listRevocations(w http.ResponseWriter, r *http.Request) {
	if h.revocations == nil {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "internal_api_unconfigured", "revocation store is not configured")
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	statuses, err := h.revocations.ListStatuses(r.Context(), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if statuses == nil {
		statuses = []revocation.Status{}
	}
	writeJSON(w, http.StatusOK, statuses)
}

func (h *handler) runRetention(w http.ResponseWriter, r *http.Request) {
	if h.retention == nil {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "internal_api_unconfigured", "retention worker is not configured")
		return
	}
	report, err := h.retention.RunOnce(r.Context())
	if err != nil {
		if errors.Is(err, retention.ErrLeaderHeld) {
			h.observeRetention("skipped", retention.Report{})
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusConflict, "retention_in_progress", "另一实例正在执行 retention sweep，请稍后重试")
			return
		}
		h.observeRetention("error", retention.Report{})
		h.fail(w, r, err)
		return
	}
	h.observeRetention("ok", report)
	writeJSON(w, http.StatusOK, report)
}

func (h *handler) retentionPolicy(w http.ResponseWriter, r *http.Request) {
	if h.retention == nil {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "internal_api_unconfigured", "retention worker is not configured")
		return
	}
	policy := h.retention.Policy()
	writeJSON(w, http.StatusOK, map[string]any{
		"membership_retention_hours":   policy.MembershipRetention.Hours(),
		"event_retention_hours":        policy.EventRetention.Hours(),
		"physical_delete_sla_hours":    policy.PhysicalDeleteSLA.Hours(),
		"physical_delete_grace_hours":  policy.PhysicalDeleteGrace.Hours(),
		"membership_retention_ceiling": "92 days",
		"event_retention_ceiling":      "365 days",
		"physical_delete_sla_ceiling":  "30 days",
	})
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	requestID := observability.RequestIDFromContext(r.Context())
	switch {
	case errors.Is(err, snapshot.ErrInvalidImport):
		writeError(w, requestID, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, snapshot.ErrJobNotFound):
		writeError(w, requestID, http.StatusNotFound, "not_found", "snapshot import job not found")
	case errors.Is(err, snapshot.ErrJobState):
		writeError(w, requestID, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, snapshot.ErrNotVerified):
		writeError(w, requestID, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, snapshot.ErrSealed):
		writeError(w, requestID, http.StatusConflict, "snapshot_sealed", "published snapshots are sealed and cannot be re-verified")
	case errors.Is(err, snapshot.ErrNoPublication):
		writeError(w, requestID, http.StatusNotFound, "not_found", "no published snapshot for this provider/namespace")
	case errors.Is(err, revocation.ErrInvalidRequest):
		writeError(w, requestID, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, revocation.ErrNotFound):
		writeError(w, requestID, http.StatusNotFound, "not_found", "revocation not found")
	case errors.Is(err, revocation.ErrStoreUnavailable):
		writeError(w, requestID, http.StatusServiceUnavailable, "dependency_unavailable", "revocation store is unavailable")
	case errors.Is(err, snapshot.ErrStoreUnavailable):
		h.logger.Error("DMP snapshot store unavailable", slog.String("request_id", requestID), slog.Any("error", err))
		writeError(w, requestID, http.StatusServiceUnavailable, "dependency_unavailable", "snapshot store is unavailable")
	default:
		h.logger.Error("DMP internal API request failed",
			slog.String("request_id", requestID), slog.String("path", r.URL.Path), slog.Any("error", err))
		writeError(w, requestID, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

func decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		message := "request body is invalid"
		if !errors.Is(err, io.EOF) {
			message += ": " + err.Error()
		}
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", message)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON object")
		return false
	}
	return true
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusNotFound, "not_found", "unknown internal API path")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusMethodNotAllowed, "method_not_allowed", "internal API method is not supported")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, requestID string, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message, "request_id": requestID})
}
