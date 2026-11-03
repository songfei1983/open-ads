package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// ManagementStore is the control-plane surface consumed by the HTTP handlers.
type ManagementStore interface {
	ListPublishers(context.Context) ([]Publisher, error)
	CreatePublisher(context.Context, PublisherInput) (Publisher, error)
	UpdatePublisher(context.Context, string, PublisherInput) (Publisher, error)
	ListProperties(context.Context) ([]Property, error)
	CreateProperty(context.Context, PropertyInput) (Property, error)
	UpdateProperty(context.Context, string, PropertyInput) (Property, error)
	ListAuctionPolicies(context.Context) ([]AuctionPolicy, error)
	CreateAuctionPolicy(context.Context, AuctionPolicyInput) (AuctionPolicy, error)
	UpdateAuctionPolicy(context.Context, string, AuctionPolicyInput) (AuctionPolicy, error)
	ListPlacements(context.Context) ([]Placement, error)
	CreatePlacement(context.Context, PlacementInput) (Placement, error)
	UpdatePlacement(context.Context, string, PlacementInput) (Placement, error)
	ListSupplyEndpoints(context.Context) ([]SupplyEndpoint, error)
	CreateSupplyEndpoint(context.Context, SupplyEndpointInput) (SupplyEndpoint, error)
	UpdateSupplyEndpoint(context.Context, string, SupplyEndpointInput) (SupplyEndpoint, error)
	ListDSPEndpoints(context.Context) ([]DSPEndpoint, error)
	CreateDSPEndpoint(context.Context, DSPEndpointInput) (DSPEndpoint, error)
	UpdateDSPEndpoint(context.Context, string, DSPEndpointInput) (DSPEndpoint, error)
	Publish(context.Context, string, string, uint64) (string, error)
	ListCreativeWhitelist(context.Context, string) (CreativeWhitelistResponse, error)
	UpsertCreativeRegistry(context.Context, string, []CreativeRegistration) (int, error)
	ListOutbox(context.Context, string, int) ([]OutboxEvent, error)
	RetryOutbox(context.Context, string) error
}

// RuntimeReader exposes the observed instance heartbeats.
type RuntimeReader interface {
	Instances(context.Context) ([]RuntimeInstance, error)
}

// NewAdminHandler builds the tenant-scoped management API. Every request is
// authenticated with the configured bearer token; no tenant is accepted from
// the caller.
func NewAdminHandler(store ManagementStore, bearerSecret string, runtime RuntimeReader, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, bearerSecret: bearerSecret, runtime: runtime, logger: logger}
	return h.authenticate(http.HandlerFunc(h.dispatchAdmin))
}

// NewInternalHandler builds the DSP-facing creative registry sync endpoint.
func NewInternalHandler(store ManagementStore, bearerSecret string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, bearerSecret: bearerSecret, logger: logger}
	return h.authenticateInternal(http.HandlerFunc(h.dispatchInternal))
}

type handler struct {
	store        ManagementStore
	bearerSecret string
	runtime      RuntimeReader
	logger       *slog.Logger
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return h.authenticateWith(next, h.bearerSecret, "admin")
}

func (h *handler) authenticateInternal(next http.Handler) http.Handler {
	return h.authenticateWith(next, h.bearerSecret, "internal")
}

func (h *handler) authenticateWith(next http.Handler, secret, scope string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if secret == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", scope+" API is not configured")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		providedHash := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
		secretHash := sha256.Sum256([]byte(secret))
		if subtle.ConstantTimeCompare(providedHash[:], secretHash[:]) != 1 {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) dispatchAdmin(w http.ResponseWriter, r *http.Request) {
	segments := splitPath(strings.TrimPrefix(r.URL.Path, "/admin/v1"))
	if len(segments) == 0 {
		writeAPIError(w, http.StatusNotFound, CodeNotFound, "resource not found")
		return
	}
	resource, id, action := segments[0], "", ""
	if len(segments) > 1 {
		id = segments[1]
	}
	if len(segments) > 2 {
		action = segments[2]
	}
	// Accept the API's action suffix form (id:publish, event:retry).
	if id != "" {
		if idx := strings.IndexByte(id, ':'); idx >= 0 {
			action, id = id[idx+1:], id[:idx]
		}
	}

	switch resource {
	case "publishers":
		h.handleCollection(w, r, id, action, AggregatePublisher,
			func(ctx context.Context, w http.ResponseWriter, r *http.Request) { h.listPublishers(w, r) },
			func(w http.ResponseWriter, r *http.Request) { h.createPublisher(w, r) },
			func(w http.ResponseWriter, r *http.Request, id string) { h.updatePublisher(w, r, id) })
	case "properties":
		h.handleCollection(w, r, id, action, AggregateProperty,
			func(ctx context.Context, w http.ResponseWriter, r *http.Request) { h.listProperties(w, r) },
			func(w http.ResponseWriter, r *http.Request) { h.createProperty(w, r) },
			func(w http.ResponseWriter, r *http.Request, id string) { h.updateProperty(w, r, id) })
	case "auction-policies":
		h.handleCollection(w, r, id, action, AggregateAuctionPolicy,
			func(ctx context.Context, w http.ResponseWriter, r *http.Request) { h.listAuctionPolicies(w, r) },
			func(w http.ResponseWriter, r *http.Request) { h.createAuctionPolicy(w, r) },
			func(w http.ResponseWriter, r *http.Request, id string) { h.updateAuctionPolicy(w, r, id) })
	case "placements":
		h.handleCollection(w, r, id, action, AggregatePlacement,
			func(ctx context.Context, w http.ResponseWriter, r *http.Request) { h.listPlacements(w, r) },
			func(w http.ResponseWriter, r *http.Request) { h.createPlacement(w, r) },
			func(w http.ResponseWriter, r *http.Request, id string) { h.updatePlacement(w, r, id) })
	case "supply-endpoints":
		h.handleCollection(w, r, id, action, AggregateSupplyEndpoint,
			func(ctx context.Context, w http.ResponseWriter, r *http.Request) { h.listSupplyEndpoints(w, r) },
			func(w http.ResponseWriter, r *http.Request) { h.createSupplyEndpoint(w, r) },
			func(w http.ResponseWriter, r *http.Request, id string) { h.updateSupplyEndpoint(w, r, id) })
	case "dsp-endpoints":
		if id != "" && action == "creative-whitelist" && r.Method == http.MethodGet {
			h.listCreativeWhitelist(w, r, id)
			return
		}
		h.handleCollection(w, r, id, action, AggregateDSPEndpoint,
			func(ctx context.Context, w http.ResponseWriter, r *http.Request) { h.listDSPEndpoints(w, r) },
			func(w http.ResponseWriter, r *http.Request) { h.createDSPEndpoint(w, r) },
			func(w http.ResponseWriter, r *http.Request, id string) { h.updateDSPEndpoint(w, r, id) })
	case "config-outbox":
		if id == "" && r.Method == http.MethodGet {
			h.listOutbox(w, r)
			return
		}
		if id != "" && (action == "retry" || action == "") && r.Method == http.MethodPost {
			h.retryOutbox(w, r, id)
			return
		}
		writeAPIError(w, http.StatusMethodNotAllowed, CodeInvalidRequest, "unsupported config-outbox method")
	case "runtime":
		if len(segments) > 1 && segments[1] == "instances" && r.Method == http.MethodGet {
			h.listRuntimeInstances(w, r)
			return
		}
		writeAPIError(w, http.StatusNotFound, CodeNotFound, "resource not found")
	default:
		writeAPIError(w, http.StatusNotFound, CodeNotFound, "resource not found")
	}
}

func (h *handler) handleCollection(
	w http.ResponseWriter,
	r *http.Request,
	id, action, aggregateType string,
	list func(context.Context, http.ResponseWriter, *http.Request),
	create func(http.ResponseWriter, *http.Request),
	update func(http.ResponseWriter, *http.Request, string),
) {
	if id == "" {
		switch r.Method {
		case http.MethodGet:
			list(r.Context(), w, r)
		case http.MethodPost:
			create(w, r)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, CodeInvalidRequest, "method not allowed")
		}
		return
	}
	if action == "publish" && r.Method == http.MethodPost {
		h.publish(w, r, aggregateType, id)
		return
	}
	if action != "" {
		writeAPIError(w, http.StatusNotFound, CodeNotFound, "resource not found")
		return
	}
	if r.Method == http.MethodPut {
		update(w, r, id)
		return
	}
	writeAPIError(w, http.StatusMethodNotAllowed, CodeInvalidRequest, "method not allowed")
}

func (h *handler) publish(w http.ResponseWriter, r *http.Request, aggregateType, id string) {
	var request PublishRequest
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeAPIError(w, http.StatusBadRequest, CodeInvalidRequest, "request body is invalid")
			return
		}
	}
	version, err := h.store.Publish(r.Context(), aggregateType, id, request.Version)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"id":             id,
		"publish_status": "pending",
		"config_version": version,
	})
}

func (h *handler) listPublishers(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListPublishers(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createPublisher(w http.ResponseWriter, r *http.Request) {
	var input PublisherInput
	if !decodeBody(w, r, &input) {
		return
	}
	created, err := h.store.CreatePublisher(r.Context(), input)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updatePublisher(w http.ResponseWriter, r *http.Request, id string) {
	var input PublisherInput
	if !decodeBody(w, r, &input) {
		return
	}
	updated, err := h.store.UpdatePublisher(r.Context(), id, input)
	h.respond(w, updated, err)
}

func (h *handler) listProperties(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListProperties(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createProperty(w http.ResponseWriter, r *http.Request) {
	var input PropertyInput
	if !decodeBody(w, r, &input) {
		return
	}
	created, err := h.store.CreateProperty(r.Context(), input)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateProperty(w http.ResponseWriter, r *http.Request, id string) {
	var input PropertyInput
	if !decodeBody(w, r, &input) {
		return
	}
	updated, err := h.store.UpdateProperty(r.Context(), id, input)
	h.respond(w, updated, err)
}

func (h *handler) listAuctionPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListAuctionPolicies(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createAuctionPolicy(w http.ResponseWriter, r *http.Request) {
	var input AuctionPolicyInput
	if !decodeBody(w, r, &input) {
		return
	}
	created, err := h.store.CreateAuctionPolicy(r.Context(), input)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateAuctionPolicy(w http.ResponseWriter, r *http.Request, id string) {
	var input AuctionPolicyInput
	if !decodeBody(w, r, &input) {
		return
	}
	updated, err := h.store.UpdateAuctionPolicy(r.Context(), id, input)
	h.respond(w, updated, err)
}

func (h *handler) listPlacements(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListPlacements(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createPlacement(w http.ResponseWriter, r *http.Request) {
	var input PlacementInput
	if !decodeBody(w, r, &input) {
		return
	}
	created, err := h.store.CreatePlacement(r.Context(), input)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updatePlacement(w http.ResponseWriter, r *http.Request, id string) {
	var input PlacementInput
	if !decodeBody(w, r, &input) {
		return
	}
	updated, err := h.store.UpdatePlacement(r.Context(), id, input)
	h.respond(w, updated, err)
}

func (h *handler) listSupplyEndpoints(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListSupplyEndpoints(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createSupplyEndpoint(w http.ResponseWriter, r *http.Request) {
	var input SupplyEndpointInput
	if !decodeBody(w, r, &input) {
		return
	}
	created, err := h.store.CreateSupplyEndpoint(r.Context(), input)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateSupplyEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	var input SupplyEndpointInput
	if !decodeBody(w, r, &input) {
		return
	}
	updated, err := h.store.UpdateSupplyEndpoint(r.Context(), id, input)
	h.respond(w, updated, err)
}

func (h *handler) listDSPEndpoints(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListDSPEndpoints(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createDSPEndpoint(w http.ResponseWriter, r *http.Request) {
	var input DSPEndpointInput
	if !decodeBody(w, r, &input) {
		return
	}
	created, err := h.store.CreateDSPEndpoint(r.Context(), input)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateDSPEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	var input DSPEndpointInput
	if !decodeBody(w, r, &input) {
		return
	}
	updated, err := h.store.UpdateDSPEndpoint(r.Context(), id, input)
	h.respond(w, updated, err)
}

func (h *handler) listCreativeWhitelist(w http.ResponseWriter, r *http.Request, id string) {
	response, err := h.store.ListCreativeWhitelist(r.Context(), id)
	h.respond(w, response, err)
}

func (h *handler) listOutbox(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, CodeInvalidRequest, "limit must be an integer")
			return
		}
		limit = parsed
	}
	events, err := h.store.ListOutbox(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *handler) retryOutbox(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.store.RetryOutbox(r.Context(), id); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h *handler) listRuntimeInstances(w http.ResponseWriter, r *http.Request) {
	if h.runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "runtime status is not configured")
		return
	}
	instances, err := h.runtime.Instances(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, instances)
}

func (h *handler) dispatchInternal(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/internal/v1"), "/")
	// dsp-endpoints/{id}/creative-registry:upsert or creative-registry/upsert
	segments := splitPath(path)
	if len(segments) >= 3 && segments[0] == "dsp-endpoints" {
		action := ""
		switch {
		case len(segments) >= 4 && segments[2] == "creative-registry":
			action = segments[3]
		case len(segments) == 3 && strings.HasPrefix(segments[2], "creative-registry:"):
			action = strings.TrimPrefix(segments[2], "creative-registry:")
		}
		if action == "upsert" {
			if r.Method != http.MethodPost {
				writeAPIError(w, http.StatusMethodNotAllowed, CodeInvalidRequest, "method not allowed")
				return
			}
			h.upsertCreativeRegistry(w, r, segments[1])
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, CodeNotFound, "resource not found")
}

func (h *handler) upsertCreativeRegistry(w http.ResponseWriter, r *http.Request, endpointID string) {
	var request CreativeRegistryUpsert
	if !decodeBody(w, r, &request) {
		return
	}
	valid := make([]CreativeRegistration, 0, len(request.Creatives))
	rejected := make([]CreativeRegistryRejection, 0)
	for i := range request.Creatives {
		entry := request.Creatives[i]
		if err := ValidateCreativeRegistration(&entry); err != nil {
			rejected = append(rejected, CreativeRegistryRejection{CreativeID: entry.CreativeID, Reason: err.Error()})
			continue
		}
		valid = append(valid, entry)
	}
	accepted, err := h.store.UpsertCreativeRegistry(r.Context(), endpointID, valid)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, CreativeRegistryResult{AcceptedCount: accepted, Rejected: rejected})
}

func (h *handler) respond(w http.ResponseWriter, value any, err error) {
	h.respondStatus(w, http.StatusOK, value, err)
}

func (h *handler) respondStatus(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, status, value)
}

func (h *handler) writeError(w http.ResponseWriter, err error) {
	var apiErr *apiError
	switch {
	case errors.As(err, &apiErr):
		writeAPIError(w, apiErr.status, apiErr.code, apiErr.message)
	case errors.Is(err, ErrVersionConflict):
		writeAPIError(w, http.StatusConflict, CodeVersionConflict, "resource version does not match")
	case errors.Is(err, ErrNotFound):
		writeAPIError(w, http.StatusNotFound, CodeNotFound, "resource not found")
	case errors.Is(err, ErrInvalidReference):
		writeAPIError(w, http.StatusUnprocessableEntity, CodeInvalidReference, "referenced resource is not published")
	case errors.Is(err, ErrRegistryNotReady):
		writeAPIError(w, http.StatusUnprocessableEntity, CodeBusinessRule, "publish the DSP endpoint before syncing its creative registry")
	default:
		h.logger.Error("SSP control plane request failed", slog.Any("error", err))
		writeAPIError(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, CodeInvalidRequest, "request body exceeds the size limit")
			return false
		}
		writeAPIError(w, http.StatusBadRequest, CodeInvalidRequest, "request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, CodeInvalidRequest, "request body must contain one JSON value")
		return false
	}
	return true
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	response := map[string]string{"code": code, "message": message}
	if requestID := w.Header().Get("X-Request-ID"); requestID != "" {
		response["request_id"] = requestID
	}
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
