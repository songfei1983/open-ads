package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/config"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/observability"
)

// Server wraps the standard library http.Server.
type Server struct {
	server  *http.Server
	logger  *slog.Logger
	cfg     *config.Config
	metrics *observability.Metrics
	client  *http.Client
	isReady atomic.Bool
}

// NewServer initializes the HTTP server with router, middlewares, and probes.
func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	s := &Server{
		logger:  logger,
		cfg:     cfg,
		metrics: &observability.Metrics{},
		client:  &http.Client{},
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	handler := s.requestIDMiddleware(s.requestDeadlineMiddleware(s.loggingMiddleware(s.recoveryMiddleware(mux))))

	s.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Mark as ready by default in skeleton
	s.isReady.Store(false)

	return s
}

// SetReady updates the readiness state of the server.
func (s *Server) SetReady(ready bool) {
	s.isReady.Store(ready)
}

// Handler returns the root http.Handler (middleware wrapped mux).
func (s *Server) Handler() http.Handler {
	return s.server.Handler
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /metrics", s.metrics.Handler)
	mux.HandleFunc("POST /sdk/v1/ad-requests", s.handleAdRequests)
	mux.HandleFunc("POST /openrtb2/auction", s.handleOpenRTBAuction)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": s.cfg.ServiceName,
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !s.isReady.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "not_ready",
			"service": s.cfg.ServiceName,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":         "ready",
		"service":        s.cfg.ServiceName,
		"config_version": s.cfg.ConfigVersion,
	})
}

func (s *Server) handleAdRequests(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	w.Header().Set("Cache-Control", "no-store")

	var req adRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_request", "message": "invalid ad request", "request_id": reqID})
		return
	}
	if req.PlacementID == "" || req.Slot.Width <= 0 || req.Slot.Height <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_request", "message": "placement and slot are required", "request_id": reqID})
		return
	}

	if req.Consent.Value != "" && s.cfg.DMPURL != "" {
		if err := s.resolveAudience(r.Context(), reqID, req); err != nil {
			s.logger.Warn("audience resolve failed", slog.String("request_id", reqID), slog.Any("error", err))
			writeNoFill(w, reqID)
			return
		}
	}
	if s.cfg.DSPURL == "" {
		writeNoFill(w, reqID)
		return
	}

	resp, err := s.requestBid(r.Context(), reqID, req)
	if err != nil || resp == nil {
		if err != nil {
			s.logger.Warn("bid request failed", slog.String("request_id", reqID), slog.Any("error", err))
		}
		writeNoFill(w, reqID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": reqID,
		"status":     "filled",
		"ad": map[string]any{
			"creative_id": resp.CreativeID,
			"format":      "image",
			"render_url":  resp.RenderURL,
			"click_url":   resp.ClickURL,
			"event_token": resp.EventToken,
			"expires_at":  time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
		},
	})
}

type adRequest struct {
	RequestID   string `json:"request_id"`
	PlacementID string `json:"placement_id"`
	Slot        struct {
		ID     string `json:"id"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"slot"`
	Page struct {
		Origin string `json:"origin"`
		Path   string `json:"path"`
	} `json:"page"`
	Consent struct {
		Source string `json:"source"`
		Value  string `json:"value"`
	} `json:"consent"`
}

type audienceResolveRequest struct {
	RequestID    string   `json:"request_id"`
	TenantID     string   `json:"tenant_id"`
	ProviderID   string   `json:"provider_id"`
	Namespace    string   `json:"namespace"`
	SubjectToken string   `json:"subject_token"`
	Purposes     []string `json:"purposes"`
	MaxSegments  int      `json:"max_segments"`
}

type bidRequest struct {
	ID   string   `json:"id"`
	Imp  []bidImp `json:"imp"`
	Site bidSite  `json:"site"`
	User *bidUser `json:"user,omitempty"`
	AT   int      `json:"at"`
	TMax int      `json:"tmax"`
	Cur  []string `json:"cur"`
}
type bidImp struct {
	ID     string `json:"id"`
	TagID  string `json:"tagid"`
	Banner struct {
		W int `json:"w"`
		H int `json:"h"`
	} `json:"banner"`
}
type bidSite struct {
	Page string `json:"page"`
}
type bidUser struct {
	ID string `json:"id"`
}
type bidResponse struct {
	ID      string `json:"id"`
	SeatBid []struct {
		Bid []struct {
			ImpID string  `json:"impid"`
			Price float64 `json:"price"`
			AdID  string  `json:"adid"`
			Adm   string  `json:"adm"`
			CID   string  `json:"cid"`
		} `json:"bid"`
	} `json:"seatbid"`
}
type selectedBid struct{ CreativeID, RenderURL, ClickURL, EventToken string }

func (s *Server) resolveAudience(ctx context.Context, reqID string, req adRequest) error {
	payload := audienceResolveRequest{RequestID: reqID, TenantID: "tenant-default", ProviderID: "dmp-provider-01", Namespace: "publisher-account-v1", SubjectToken: req.Consent.Value, Purposes: []string{"personalized_ads"}, MaxSegments: 10}
	return s.postJSON(ctx, s.cfg.DMPURL+"/internal/v1/audiences:resolve", payload, &struct{}{}, s.cfg.DMPTimeout)
}

func (s *Server) requestBid(ctx context.Context, reqID string, req adRequest) (*selectedBid, error) {
	payload := bidRequest{ID: reqID, AT: 1, TMax: int(s.cfg.AuctionTimeout / time.Millisecond), Cur: []string{"USD"}, Site: bidSite{Page: req.Page.Origin + req.Page.Path}, Imp: []bidImp{{ID: "1", TagID: req.PlacementID}}}
	payload.Imp[0].Banner.W, payload.Imp[0].Banner.H = req.Slot.Width, req.Slot.Height
	if req.Consent.Value != "" {
		payload.User = &bidUser{ID: req.Consent.Value}
	}
	var response bidResponse
	if err := s.postJSON(ctx, s.cfg.DSPURL+"/openrtb2/bid", payload, &response, s.cfg.DSPTimeout); err != nil {
		return nil, err
	}
	if response.ID != "" && response.ID != reqID {
		return nil, fmt.Errorf("bid response id mismatch")
	}
	for _, seat := range response.SeatBid {
		for _, bid := range seat.Bid {
			if bid.ImpID == "1" && bid.Price > 0 && bid.Adm != "" {
				return &selectedBid{CreativeID: bid.CID, RenderURL: extractIframeURL(bid.Adm), EventToken: bid.AdID}, nil
			}
		}
	}
	return nil, nil
}

func (s *Server) postJSON(ctx context.Context, url string, payload, target any, timeout time.Duration) error {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if requestID := observability.RequestIDFromContext(ctx); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("downstream status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func extractIframeURL(adm string) string {
	const prefix = `src="`
	start := strings.Index(adm, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.IndexByte(adm[start:], '"')
	if end < 0 {
		return ""
	}
	return adm[start : start+end]
}

func writeNoFill(w http.ResponseWriter, reqID string) {
	writeJSON(w, http.StatusOK, map[string]any{"request_id": reqID, "status": "no_fill"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func (s *Server) handleOpenRTBAuction(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	var request struct {
		ID  string            `json:"id"`
		Imp []json.RawMessage `json:"imp"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil || json.Unmarshal(body, &request) != nil || request.ID == "" || len(request.Imp) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_request", "message": "invalid OpenRTB request", "request_id": reqID})
		return
	}
	if s.cfg.DSPURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"id": request.ID, "nbr": 2})
		return
	}
	var response json.RawMessage
	callCtx := observability.WithRequestID(r.Context(), request.ID)
	if err := s.postJSON(callCtx, s.cfg.DSPURL+"/openrtb2/bid", json.RawMessage(body), &response, s.cfg.DSPTimeout); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": request.ID, "nbr": 2})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}

// Start runs the HTTP server.
func (s *Server) Start() error {
	s.isReady.Store(true)
	s.logger.Info("starting HTTP server", slog.String("addr", s.server.Addr))
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down HTTP server")
	s.isReady.Store(false)
	return s.server.Shutdown(ctx)
}

func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if domain.ValidateUUID32(reqID) != nil {
			var err error
			reqID, err = domain.NewUUIDv7()
			if err != nil {
				http.Error(w, "unable to generate request ID", http.StatusInternalServerError)
				return
			}
		}

		ctx := observability.WithRequestID(r.Context(), reqID)
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requestDeadlineMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AuctionTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		reqID := observability.RequestIDFromContext(r.Context())

		s.logger.Info("http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rw.statusCode),
			slog.Int64("duration_ms", duration.Milliseconds()),
			slog.String("request_id", reqID),
		)
		s.metrics.Observe(rw.statusCode, duration)
	})
}

func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				reqID := observability.RequestIDFromContext(r.Context())
				s.logger.Error("panic recovered",
					slog.Any("recover", rec),
					slog.String("request_id", reqID),
				)
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"code":       "internal_error",
					"message":    "An unexpected error occurred.",
					"request_id": reqID,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.headerWritten {
		return
	}
	rw.headerWritten = true
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(body []byte) (int, error) {
	if !rw.headerWritten {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
