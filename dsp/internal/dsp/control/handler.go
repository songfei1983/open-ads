package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/assets"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

// NewHandler creates the tenant-scoped management API. Requests require the
// configured bearer secret; tenant identity comes from server configuration.
func NewHandler(store *SQLStore, bearerSecret string) http.Handler {
	h := &handler{store: store, bearerSecret: bearerSecret}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/advertisers", h.listAdvertisers)
	mux.HandleFunc("POST /admin/v1/advertisers", h.createAdvertiser)
	mux.HandleFunc("PUT /admin/v1/advertisers/{id}", h.updateAdvertiser)
	mux.HandleFunc("GET /admin/v1/campaigns", h.listCampaigns)
	mux.HandleFunc("POST /admin/v1/campaigns", h.createCampaign)
	mux.HandleFunc("PUT /admin/v1/campaigns/{id}", h.updateCampaign)
	mux.HandleFunc("POST /admin/v1/campaigns/{id}:publish", h.publishCampaign)
	mux.HandleFunc("GET /admin/v1/line-items", h.listLineItems)
	mux.HandleFunc("POST /admin/v1/line-items", h.createLineItem)
	mux.HandleFunc("PUT /admin/v1/line-items/{id}", h.updateLineItem)
	mux.HandleFunc("POST /admin/v1/line-items/{id}:publish", h.publishLineItem)
	mux.HandleFunc("GET /admin/v1/creatives", h.listCreatives)
	mux.HandleFunc("POST /admin/v1/creatives", h.createCreative)
	mux.HandleFunc("PUT /admin/v1/creatives/{id}", h.updateCreative)
	mux.HandleFunc("POST /admin/v1/creatives/{id}:publish", h.publishCreative)
	mux.HandleFunc("POST /admin/v1/creative-assets", h.uploadAsset)
	mux.HandleFunc("GET /admin/v1/budgets", h.listBudgets)
	mux.HandleFunc("GET /admin/v1/quarantine", h.listQuarantine)
	mux.HandleFunc("POST /admin/v1/quarantine/{id}:resolve", h.resolveQuarantine)
	mux.HandleFunc("GET /admin/v1/status", h.getStatus)
	return h.authenticate(mux)
}

type handler struct {
	store        *SQLStore
	bearerSecret string
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if h.bearerSecret == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "management API is not configured")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		providedHash := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
		secretHash := sha256.Sum256([]byte(h.bearerSecret))
		if subtle.ConstantTimeCompare(providedHash[:], secretHash[:]) != 1 {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) listAdvertisers(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListAdvertisers(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createAdvertiser(w http.ResponseWriter, r *http.Request) {
	var item Advertiser
	if !decodeBody(w, r, &item) {
		return
	}
	created, err := h.store.CreateAdvertiser(r.Context(), item)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateAdvertiser(w http.ResponseWriter, r *http.Request) {
	var item Advertiser
	if !decodeBody(w, r, &item) {
		return
	}
	updated, err := h.store.UpdateAdvertiser(r.Context(), r.PathValue("id"), item)
	h.respond(w, updated, err)
}

func (h *handler) listCampaigns(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListCampaigns(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createCampaign(w http.ResponseWriter, r *http.Request) {
	var item Campaign
	if !decodeBody(w, r, &item) {
		return
	}
	created, err := h.store.CreateCampaign(r.Context(), item)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateCampaign(w http.ResponseWriter, r *http.Request) {
	var item Campaign
	if !decodeBody(w, r, &item) {
		return
	}
	updated, err := h.store.UpdateCampaign(r.Context(), r.PathValue("id"), item)
	h.respond(w, updated, err)
}

func (h *handler) publishCampaign(w http.ResponseWriter, r *http.Request) {
	h.publish(w, r, "campaign")
}

func (h *handler) listLineItems(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListLineItems(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createLineItem(w http.ResponseWriter, r *http.Request) {
	var item LineItem
	if !decodeBody(w, r, &item) {
		return
	}
	created, err := h.store.CreateLineItem(r.Context(), item)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateLineItem(w http.ResponseWriter, r *http.Request) {
	var item LineItem
	if !decodeBody(w, r, &item) {
		return
	}
	updated, err := h.store.UpdateLineItem(r.Context(), r.PathValue("id"), item)
	h.respond(w, updated, err)
}

func (h *handler) publishLineItem(w http.ResponseWriter, r *http.Request) {
	h.publish(w, r, "line_item")
}

func (h *handler) listCreatives(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListCreatives(r.Context())
	h.respond(w, items, err)
}

func (h *handler) createCreative(w http.ResponseWriter, r *http.Request) {
	var item CreativeInput
	if !decodeBody(w, r, &item) {
		return
	}
	created, err := h.store.CreateCreative(r.Context(), item)
	h.respondStatus(w, http.StatusCreated, created, err)
}

func (h *handler) updateCreative(w http.ResponseWriter, r *http.Request) {
	var item CreativeInput
	if !decodeBody(w, r, &item) {
		return
	}
	updated, err := h.store.UpdateCreative(r.Context(), r.PathValue("id"), item)
	h.respond(w, updated, err)
}

func (h *handler) publishCreative(w http.ResponseWriter, r *http.Request) {
	h.publish(w, r, "creative")
}

func (h *handler) uploadAsset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, assets.MaxContentBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "asset_too_large", "asset upload exceeds the size limit")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "multipart asset upload is invalid")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if r.MultipartForm == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "multipart asset upload is invalid")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "exactly one file field is required")
		return
	}
	if len(r.MultipartForm.Value["expected_sha256"]) > 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "expected_sha256 must occur at most once")
		return
	}
	for field := range r.MultipartForm.Value {
		if field != "expected_sha256" {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "unsupported multipart field")
			return
		}
	}
	file, err := files[0].Open()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "uploaded file is unavailable")
		return
	}
	defer file.Close()
	expectedHash := ""
	if values := r.MultipartForm.Value["expected_sha256"]; len(values) == 1 {
		expectedHash = values[0]
	}
	metadata, err := h.store.UploadAsset(r.Context(), file, expectedHash)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, metadata)
}

func (h *handler) publish(w http.ResponseWriter, r *http.Request, resourceType string) {
	var request struct {
		Version uint64 `json:"version"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	version, err := h.store.Publish(r.Context(), resourceType, r.PathValue("id"), request.Version)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"id":             r.PathValue("id"),
		"publish_status": "pending",
		"config_version": version,
	})
}

func (h *handler) listBudgets(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListBudgets(r.Context())
	h.respond(w, items, err)
}

func (h *handler) listQuarantine(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListQuarantine(r.Context())
	h.respond(w, items, err)
}

func (h *handler) resolveQuarantine(w http.ResponseWriter, r *http.Request) {
	err := h.store.ResolveQuarantine(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"quarantine_id": r.PathValue("id"),
		"status":        "resolved",
	})
}

func (h *handler) getStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.store.GetStatus(r.Context(), snapshot.CurrentVersion())
	h.respond(w, status, err)
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
	switch {
	case errors.Is(err, ErrVersionConflict):
		writeAPIError(w, http.StatusConflict, "version_conflict", "resource version does not match")
	case errors.Is(err, ErrUnsupportedTargeting):
		writeAPIError(w, http.StatusUnprocessableEntity, "unsupported_targeting", "targeting condition is not supported")
	case errors.Is(err, ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, assets.ErrAssetNotFound):
		writeAPIError(w, http.StatusNotFound, "asset_not_found", "uploaded asset is not registered")
	case errors.Is(err, assets.ErrTooLarge):
		writeAPIError(w, http.StatusUnprocessableEntity, "asset_too_large", "asset exceeds the size limit")
	case errors.Is(err, assets.ErrUnsupportedMIME):
		writeAPIError(w, http.StatusUnprocessableEntity, "unsupported_mime_type", "asset MIME type is not supported")
	case errors.Is(err, assets.ErrDimensionsExceeded):
		writeAPIError(w, http.StatusUnprocessableEntity, "asset_dimensions_exceeded", "asset dimensions exceed the limit")
	case errors.Is(err, assets.ErrHashMismatch):
		writeAPIError(w, http.StatusUnprocessableEntity, "asset_hash_mismatch", "uploaded asset does not match expected_sha256")
	case errors.Is(err, assets.ErrInvalidImage):
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_image", "uploaded bytes are not a valid supported image")
	case errors.Is(err, assets.ErrInvalidAssetRef):
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "asset reference or digest is invalid")
	case errors.Is(err, assets.ErrAssetTampered):
		writeAPIError(w, http.StatusServiceUnavailable, "asset_tampered_detected", "stored asset failed integrity verification")
	case errors.Is(err, ErrInvalidConfiguration):
		writeAPIError(w, http.StatusUnprocessableEntity, "business_rule_violation", err.Error())
	case errors.Is(err, domain.ErrInvalidUUID):
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "resource id is invalid")
	default:
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			writeAPIError(w, http.StatusConflict, "resource_conflict", "resource already exists")
			return
		}
		if errors.As(err, &mysqlError) && mysqlError.Number == 1452 {
			writeAPIError(w, http.StatusUnprocessableEntity, "invalid_reference", "referenced resource does not exist")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body exceeds the size limit")
			return false
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value")
		return false
	}
	return true
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
