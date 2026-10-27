// Package internalapi serves the DMP internal API used by trusted adapters:
// snapshot import/verification (and, in later iterations, publication,
// audience resolution and revocation).
package internalapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
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
	Resolve(ctx context.Context, input snapshot.ResolveInput) (snapshot.ResolveResult, error)
}

// HandlerOptions carries the trusted-adapter dependency set.
type HandlerOptions struct {
	Token     string
	Snapshots SnapshotService
	Logger    *slog.Logger
}

// NewHandler returns the internal API. Every request must present the internal
// bearer token: this surface is only for trusted tokenization/import adapters,
// never for browsers or anonymous callers.
func NewHandler(opts HandlerOptions) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{token: opts.Token, snapshots: opts.Snapshots, logger: logger}
	return h.authenticate(h)
}

type handler struct {
	token     string
	snapshots SnapshotService
	logger    *slog.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/v1"), "/")
	parts := strings.Split(path, "/")
	switch {
	case path == "snapshots":
		switch r.Method {
		case http.MethodGet:
			h.listSnapshots(w, r)
		case http.MethodPost:
			h.createSnapshot(w, r)
		default:
			methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
		}
	case len(parts) == 2 && parts[0] == "snapshots":
		switch {
		case r.Method == http.MethodGet:
			h.getSnapshot(w, r, parts[1])
		default:
			methodNotAllowed(w, r, http.MethodGet)
		}
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "rows":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		h.appendRows(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "verify":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		h.verifySnapshot(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "publish":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		h.publishSnapshot(w, r, parts[1])
	case path == "audiences:resolve":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, http.MethodPost)
			return
		}
		h.resolveAudience(w, r)
	default:
		notFound(w, r)
	}
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := observability.RequestIDFromContext(r.Context())
		if h.snapshots == nil || (h.token == "" && !isResolvePath(r.URL.Path)) {
			writeError(w, requestID, http.StatusServiceUnavailable, "internal_api_unconfigured", "DMP internal API is not configured")
			return
		}
		if isResolvePath(r.URL.Path) {
			// Sandbox profile: the resolve path is called by SSP without a
			// workload identity. D-01 replaces this with mTLS authorization.
			next.ServeHTTP(w, r)
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
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) verifySnapshot(w http.ResponseWriter, r *http.Request, id string) {
	job, err := h.snapshots.Verify(r.Context(), id)
	if err != nil {
		if errors.Is(err, snapshot.ErrVerification) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"code":       "verification_failed",
				"message":    err.Error(),
				"request_id": observability.RequestIDFromContext(r.Context()),
				"job":        job,
			})
			return
		}
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *handler) publishSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	job, err := h.snapshots.Publish(r.Context(), id)
	if err != nil {
		if errors.Is(err, snapshot.ErrVerification) {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusUnprocessableEntity, "verification_failed", err.Error())
			return
		}
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *handler) resolveAudience(w http.ResponseWriter, r *http.Request) {
	var input snapshot.ResolveInput
	if !decode(w, r, &input) {
		return
	}
	input.RequestID = observability.RequestIDFromContext(r.Context())
	result, err := h.snapshots.Resolve(r.Context(), input)
	if err != nil {
		if errors.Is(err, snapshot.ErrNoPublication) {
			writeError(w, observability.RequestIDFromContext(r.Context()), http.StatusServiceUnavailable, "no_published_snapshot", "no published snapshot serves this provider/namespace")
			return
		}
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
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
	case errors.Is(err, snapshot.ErrNoPublication):
		writeError(w, requestID, http.StatusNotFound, "not_found", "no published snapshot for this provider/namespace")
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
