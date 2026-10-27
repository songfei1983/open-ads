package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

// HandlerOptions binds the admin surface to one tenant and one bearer token.
type HandlerOptions struct {
	Store  Store
	Token  string
	Actor  string
	Logger *slog.Logger
}

// NewHandler returns the DMP management API: versioned definitions behind a
// tenant-scoped bearer token. An unconfigured token disables the whole surface
// rather than exposing unauthenticated writes.
func NewHandler(opts HandlerOptions) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: opts.Store, token: opts.Token, actor: opts.Actor, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/namespaces", h.listNamespaces)
	mux.HandleFunc("POST /admin/v1/namespaces", h.createNamespace)
	mux.HandleFunc("PUT /admin/v1/namespaces/{id}", h.updateNamespace)
	mux.HandleFunc("GET /admin/v1/providers", h.listProviders)
	mux.HandleFunc("POST /admin/v1/providers", h.createProvider)
	mux.HandleFunc("PUT /admin/v1/providers/{id}", h.updateProvider)
	mux.HandleFunc("GET /admin/v1/segments", h.listSegments)
	mux.HandleFunc("POST /admin/v1/segments", h.createSegment)
	mux.HandleFunc("PUT /admin/v1/segments/{id}", h.updateSegment)
	mux.HandleFunc("GET /admin/v1/audit", h.listAudit)
	return h.authenticate(mux)
}

type handler struct {
	store  Store
	token  string
	actor  string
	logger *slog.Logger
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

func (h *handler) listNamespaces(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListNamespaces(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []Namespace{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *handler) createNamespace(w http.ResponseWriter, r *http.Request) {
	var input NamespaceInput
	if !decode(w, r, &input) {
		return
	}
	if err := input.Validate(); err != nil {
		h.reject(w, r, err)
		return
	}
	created, err := h.store.CreateNamespace(r.Context(), input, h.mutation(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) updateNamespace(w http.ResponseWriter, r *http.Request) {
	var input NamespaceInput
	if !decode(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	if input.Version == 0 {
		h.reject(w, r, errors.New("version is required"))
		return
	}
	if err := input.Validate(); err != nil {
		h.reject(w, r, err)
		return
	}
	updated, err := h.store.UpdateNamespace(r.Context(), input.ID, input, h.mutation(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *handler) listProviders(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListProviders(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []Provider{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var input ProviderInput
	if !decode(w, r, &input) {
		return
	}
	if err := input.Validate(); err != nil {
		h.reject(w, r, err)
		return
	}
	created, err := h.store.CreateProvider(r.Context(), input, h.mutation(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	var input ProviderInput
	if !decode(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	if input.Version == 0 {
		h.reject(w, r, errors.New("version is required"))
		return
	}
	if err := input.Validate(); err != nil {
		h.reject(w, r, err)
		return
	}
	updated, err := h.store.UpdateProvider(r.Context(), input.ID, input, h.mutation(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *handler) listSegments(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListSegments(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []Segment{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *handler) createSegment(w http.ResponseWriter, r *http.Request) {
	var input SegmentInput
	if !decode(w, r, &input) {
		return
	}
	if err := input.Validate(); err != nil {
		h.reject(w, r, err)
		return
	}
	created, err := h.store.CreateSegment(r.Context(), input, h.mutation(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handler) updateSegment(w http.ResponseWriter, r *http.Request) {
	var input SegmentInput
	if !decode(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	if input.Version == 0 {
		h.reject(w, r, errors.New("version is required"))
		return
	}
	if err := input.Validate(); err != nil {
		h.reject(w, r, err)
		return
	}
	updated, err := h.store.UpdateSegment(r.Context(), input.ID, input, h.mutation(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
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
		h.fail(w, r, err)
		return
	}
	if entries == nil {
		entries = []AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *handler) mutation(r *http.Request) Mutation {
	return Mutation{Actor: h.actor, RequestID: observability.RequestIDFromContext(r.Context())}
}

func (h *handler) reject(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, ErrInvalidDefinition) {
		err = errors.Join(ErrInvalidDefinition, err)
	}
	if auditErr := h.store.RecordRejected(r.Context(), h.mutation(r), actionFor(r.Method), resourceTypeFor(r.URL.Path), r.PathValue("id"), err.Error()); auditErr != nil {
		h.logger.Warn("DMP control plane rejection could not be audited",
			slog.String("request_id", observability.RequestIDFromContext(r.Context())),
			slog.Any("error", auditErr))
	}
	writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusUnprocessableEntity, "invalid_definition", err.Error())
}

func actionFor(method string) string {
	if method == http.MethodPost {
		return ActionCreate
	}
	return ActionUpdate
}

func resourceTypeFor(path string) string {
	switch {
	case strings.Contains(path, "/namespaces"):
		return "namespace"
	case strings.Contains(path, "/providers"):
		return "provider"
	default:
		return "segment"
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
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
	default:
		h.logger.Error("DMP control plane request failed",
			slog.String("request_id", requestID),
			slog.String("path", r.URL.Path),
			slog.Any("error", err))
		writeError(w, requestID, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
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
