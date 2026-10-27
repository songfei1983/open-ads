package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

const adminPrefix = "/admin/v1"

// unfinishedIntentGrace keeps an in-flight attempt out of the unfinished report.
const unfinishedIntentGrace = 2 * time.Minute

// HandlerOptions binds the admin surface to one tenant and one bearer token.
type HandlerOptions struct {
	Store  Store
	Token  string
	Actor  string
	Logger *slog.Logger
}

// NewHandler returns the DMP management API: versioned definitions behind a
// tenant-scoped bearer token. An unconfigured token disables the whole surface
// rather than exposing unauthenticated writes. Routing is explicit so unknown
// paths and unsupported methods answer with the same JSON error envelope as the
// rest of the API instead of net/http's plain-text defaults.
func NewHandler(opts HandlerOptions) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: opts.Store, token: opts.Token, actor: opts.Actor, logger: logger}
	return h.authenticate(h)
}

type handler struct {
	store  Store
	token  string
	actor  string
	logger *slog.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, adminPrefix), "/")
	if path == "audit" || path == "audit/unfinished" {
		if r.Method != http.MethodGet {
			h.methodNotAllowed(w, r, http.MethodGet)
			return
		}
		if path == "audit/unfinished" {
			h.listUnfinishedIntents(w, r)
			return
		}
		h.listAudit(w, r)
		return
	}
	if path == "maintenance/duplicate-versions" {
		switch r.Method {
		case http.MethodGet:
			h.reportDuplicateVersions(w, r)
		case http.MethodPost:
			h.pruneDuplicateVersions(w, r)
		default:
			h.methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
		}
		return
	}

	parts := strings.Split(path, "/")
	if path == "" || parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] == "") {
		h.notFound(w, r)
		return
	}
	collection := parts[0]
	if collection != "namespaces" && collection != "providers" && collection != "segments" {
		h.notFound(w, r)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			h.list(w, r, collection)
		case http.MethodPost:
			h.create(w, r, collection)
		default:
			h.methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
		}
		return
	}

	if r.Method != http.MethodPut {
		h.methodNotAllowed(w, r, http.MethodPut)
		return
	}
	h.update(w, r, collection, parts[1])
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := observability.RequestIDFromContext(r.Context())
		if h.token == "" || h.store == nil {
			writeError(w, requestID, http.StatusServiceUnavailable, "control_plane_unavailable", "DMP control plane is not configured")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeError(w, requestID, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		provided := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
		expected := sha256.Sum256([]byte(h.token))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			writeError(w, requestID, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, collection string) {
	switch collection {
	case "namespaces":
		namespaces, err := h.store.ListNamespaces(r.Context())
		if err != nil {
			h.fail(w, r, "namespace", "", err)
			return
		}
		if namespaces == nil {
			namespaces = []Namespace{}
		}
		writeJSON(w, http.StatusOK, namespaces)
	case "providers":
		providers, err := h.store.ListProviders(r.Context())
		if err != nil {
			h.fail(w, r, "provider", "", err)
			return
		}
		if providers == nil {
			providers = []Provider{}
		}
		writeJSON(w, http.StatusOK, providers)
	case "segments":
		segments, err := h.store.ListSegments(r.Context())
		if err != nil {
			h.fail(w, r, "segment", "", err)
			return
		}
		if segments == nil {
			segments = []Segment{}
		}
		writeJSON(w, http.StatusOK, segments)
	}
}

func (h *handler) create(w http.ResponseWriter, r *http.Request, collection string) {
	switch collection {
	case "namespaces":
		var input NamespaceInput
		if !decode(w, r, &input) {
			return
		}
		if err := input.Validate(); err != nil {
			h.reject(w, r, ActionCreate, "namespace", input.ID, err)
			return
		}
		created, err := h.store.CreateNamespace(r.Context(), input, h.mutation(r))
		h.respond(w, r, "namespace", input.ID, http.StatusCreated, created, err)
	case "providers":
		var input ProviderInput
		if !decode(w, r, &input) {
			return
		}
		if err := input.Validate(); err != nil {
			h.reject(w, r, ActionCreate, "provider", input.ID, err)
			return
		}
		created, err := h.store.CreateProvider(r.Context(), input, h.mutation(r))
		h.respond(w, r, "provider", input.ID, http.StatusCreated, created, err)
	case "segments":
		var input SegmentInput
		if !decode(w, r, &input) {
			return
		}
		if err := input.Validate(); err != nil {
			h.reject(w, r, ActionCreate, "segment", input.ID, err)
			return
		}
		created, err := h.store.CreateSegment(r.Context(), input, h.mutation(r))
		h.respond(w, r, "segment", input.ID, http.StatusCreated, created, err)
	}
}

func (h *handler) update(w http.ResponseWriter, r *http.Request, collection, id string) {
	switch collection {
	case "namespaces":
		var input NamespaceInput
		if !decode(w, r, &input) {
			return
		}
		input.ID = id
		if !h.checkVersion(w, r, ActionUpdate, "namespace", id, input.Version) {
			return
		}
		if err := input.Validate(); err != nil {
			h.reject(w, r, ActionUpdate, "namespace", id, err)
			return
		}
		updated, err := h.store.UpdateNamespace(r.Context(), id, input, h.mutation(r))
		h.respond(w, r, "namespace", id, http.StatusOK, updated, err)
	case "providers":
		var input ProviderInput
		if !decode(w, r, &input) {
			return
		}
		input.ID = id
		if !h.checkVersion(w, r, ActionUpdate, "provider", id, input.Version) {
			return
		}
		if err := input.Validate(); err != nil {
			h.reject(w, r, ActionUpdate, "provider", id, err)
			return
		}
		updated, err := h.store.UpdateProvider(r.Context(), id, input, h.mutation(r))
		h.respond(w, r, "provider", id, http.StatusOK, updated, err)
	case "segments":
		var input SegmentInput
		if !decode(w, r, &input) {
			return
		}
		input.ID = id
		if !h.checkVersion(w, r, ActionUpdate, "segment", id, input.Version) {
			return
		}
		if err := input.Validate(); err != nil {
			h.reject(w, r, ActionUpdate, "segment", id, err)
			return
		}
		updated, err := h.store.UpdateSegment(r.Context(), id, input, h.mutation(r))
		h.respond(w, r, "segment", id, http.StatusOK, updated, err)
	}
}

func (h *handler) checkVersion(w http.ResponseWriter, r *http.Request, action, resourceType, resourceID string, version uint64) bool {
	if version != 0 {
		return true
	}
	h.reject(w, r, action, resourceType, resourceID, errors.New("version is required"))
	return false
}

func (h *handler) listAudit(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	entries, err := h.store.ListAudit(r.Context(), AuditQuery{
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
		Limit:        limit,
	})
	if err != nil {
		h.fail(w, r, "audit", "", err)
		return
	}
	if entries == nil {
		entries = []AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// listUnfinishedIntents reports attempts that never recorded an outcome, so an
// operator can reconcile writes whose result is unknown.
func (h *handler) listUnfinishedIntents(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	entries, err := h.store.UnfinishedIntents(r.Context(), unfinishedIntentGrace, limit)
	if err != nil {
		h.fail(w, r, "audit", "", err)
		return
	}
	if entries == nil {
		entries = []AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// maintenance returns the optional store maintenance surface.
func (h *handler) maintenance(w http.ResponseWriter, r *http.Request) (Maintenance, bool) {
	store, ok := h.store.(Maintenance)
	if !ok {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "control_plane_unavailable", "definition store does not support maintenance")
		return nil, false
	}
	return store, true
}

// reportDuplicateVersions counts losing rows without deleting anything.
func (h *handler) reportDuplicateVersions(w http.ResponseWriter, r *http.Request) {
	store, ok := h.maintenance(w, r)
	if !ok {
		return
	}
	groups, err := store.FindDuplicateVersions(r.Context())
	if err != nil {
		h.fail(w, r, "maintenance", "", err)
		return
	}
	if groups == nil {
		groups = []DuplicateVersion{}
	}
	report := PruneReport{DryRun: true, Groups: groups}
	for _, group := range groups {
		report.RowsToDelete += group.Rows - 1
	}
	writeJSON(w, http.StatusOK, report)
}

// pruneDuplicateVersions deletes losing rows. It defaults to a dry run so an
// accidental call never mutates storage.
func (h *handler) pruneDuplicateVersions(w http.ResponseWriter, r *http.Request) {
	store, ok := h.maintenance(w, r)
	if !ok {
		return
	}
	input := struct {
		DryRun *bool `json:"dry_run"`
	}{}
	if r.ContentLength > 0 {
		if !decode(w, r, &input) {
			return
		}
	}
	dryRun := true
	if input.DryRun != nil {
		dryRun = *input.DryRun
	}
	report, err := store.PruneDuplicateVersions(r.Context(), dryRun)
	if err != nil {
		h.fail(w, r, "maintenance", "", err)
		return
	}
	if report.Groups == nil {
		report.Groups = []DuplicateVersion{}
	}
	h.logger.Info("DMP duplicate version sweep",
		slog.String("request_id", observability.RequestIDFromContext(r.Context())),
		slog.Bool("dry_run", report.DryRun),
		slog.Uint64("groups", uint64(len(report.Groups))),
		slog.Uint64("deleted_rows", report.DeletedRows))
	writeJSON(w, http.StatusOK, report)
}

func (h *handler) respond(w http.ResponseWriter, r *http.Request, resourceType, resourceID string, status int, value any, err error) {
	if err != nil {
		h.fail(w, r, resourceType, resourceID, err)
		return
	}
	writeJSON(w, status, value)
}

func (h *handler) mutation(r *http.Request) Mutation {
	return Mutation{Actor: h.actor, RequestID: observability.RequestIDFromContext(r.Context())}
}

func (h *handler) reject(w http.ResponseWriter, r *http.Request, action, resourceType, resourceID string, err error) {
	if !errors.Is(err, ErrInvalidDefinition) {
		err = fmt.Errorf("%w: %s", ErrInvalidDefinition, err)
	}
	if auditErr := h.store.RecordRejected(r.Context(), h.mutation(r), action, resourceType, resourceID, err.Error()); auditErr != nil {
		// A refusal that cannot be audited must not be reported as a validation
		// outcome: the caller would otherwise probe the API untraced.
		h.logger.Error("DMP control plane rejection could not be audited",
			slog.String("request_id", observability.RequestIDFromContext(r.Context())),
			slog.Any("error", auditErr))
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "control_plane_unavailable", "DMP audit trail is unavailable")
		return
	}
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusUnprocessableEntity, "invalid_definition", err.Error())
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, resourceType, resourceID string, err error) {
	requestID := observability.RequestIDFromContext(r.Context())
	switch {
	case errors.Is(err, ErrVersionConflict):
		writeError(w, requestID, http.StatusConflict, "version_conflict", "definition version does not match the current version")
	case errors.Is(err, ErrAlreadyExists):
		writeError(w, requestID, http.StatusConflict, "resource_conflict", "definition already exists")
	case errors.Is(err, ErrNotFound):
		writeError(w, requestID, http.StatusNotFound, "not_found", "definition not found")
	case errors.Is(err, ErrImmutableField):
		writeError(w, requestID, http.StatusUnprocessableEntity, "immutable_field", err.Error())
	case errors.Is(err, ErrInvalidDefinition):
		writeError(w, requestID, http.StatusUnprocessableEntity, "invalid_definition", err.Error())
	case errors.Is(err, ErrStoreUnavailable):
		h.logger.Error("DMP control plane store unavailable",
			slog.String("request_id", requestID),
			slog.String("resource_type", resourceType),
			slog.String("resource_id", resourceID),
			slog.Any("error", err))
		writeError(w, requestID, http.StatusServiceUnavailable, "control_plane_unavailable", "DMP definition store is unavailable")
	default:
		h.logger.Error("DMP control plane request failed",
			slog.String("request_id", requestID),
			slog.String("path", r.URL.Path),
			slog.Any("error", err))
		writeError(w, requestID, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusNotFound, "not_found", "unknown management API path")
}

func (h *handler) methodNotAllowed(w http.ResponseWriter, r *http.Request, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusMethodNotAllowed, "method_not_allowed", "management API method is not supported")
}

func decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if errors.Is(err, io.EOF) {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "request body is required")
			return false
		}
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "request body is invalid: "+err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, requestID string, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":       code,
		"message":    message,
		"request_id": requestID,
	})
}
