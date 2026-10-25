package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/config"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/decision"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/observability"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

// Server wraps the standard library http.Server for DSP.
type Server struct {
	server       *http.Server
	logger       *slog.Logger
	cfg          *config.Config
	metrics      *observability.Metrics
	decisioner   decision.Decisioner
	adminHandler http.Handler
	isReady      atomic.Bool
}

// NewServer initializes the HTTP server with router, middlewares, and probes.
func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	s := &Server{
		logger:     logger,
		cfg:        cfg,
		metrics:    &observability.Metrics{},
		decisioner: decision.NoBidDecisioner{},
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

	s.isReady.Store(false)

	return s
}

// SetReady updates the readiness state of the server.
func (s *Server) SetReady(ready bool) {
	s.isReady.Store(ready)
}

// Handler returns the root http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.server.Handler
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /metrics", s.metrics.Handler)
	mux.HandleFunc("POST /openrtb2/bid", s.handleOpenRTBBid)
	mux.HandleFunc("POST /rtb/v1/win/{bid_id}", s.handleWinNotice)
	mux.HandleFunc("POST /rtb/v1/loss/{bid_id}", s.handleLossNotice)
	mux.HandleFunc("POST /rtb/v1/billable/{bid_id}", s.handleBillableNotice)
	mux.Handle("/admin/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminHandler == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"code": "admin_unavailable", "message": "management API is not configured",
			})
			return
		}
		s.adminHandler.ServeHTTP(w, r)
	}))
}

// SetAdminHandler installs a tenant-authenticated management API handler.
func (s *Server) SetAdminHandler(handler http.Handler) {
	s.adminHandler = handler
}

// SetDecisioner replaces the no-bid default before serving requests.
func (s *Server) SetDecisioner(decisioner decision.Decisioner) {
	if decisioner != nil {
		s.decisioner = decisioner
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": s.cfg.ServiceName,
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	version := snapshot.CurrentVersion()
	if !s.isReady.Load() || version == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":           "not_ready",
			"service":          s.cfg.ServiceName,
			"snapshot_version": version,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":           "ready",
		"service":          s.cfg.ServiceName,
		"snapshot_version": version,
	})
}

func (s *Server) handleOpenRTBBid(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	var req struct {
		ID   string            `json:"id"`
		Imp  []json.RawMessage `json:"imp"`
		Cur  []string          `json:"cur"`
		Site *struct {
			Domain string `json:"domain"`
			Page   string `json:"page"`
		} `json:"site"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil || req.ID == "" || len(req.Imp) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":       "invalid_request",
			"message":    "invalid OpenRTB request",
			"request_id": reqID,
		})
		return
	}

	var banner struct {
		Banner *struct {
			Width  int `json:"w"`
			Height int `json:"h"`
		} `json:"banner"`
	}
	if err := json.Unmarshal(req.Imp[0], &banner); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":       "invalid_request",
			"message":    "invalid OpenRTB impression",
			"request_id": reqID,
		})
		return
	}
	var siteDomain, sitePage string
	if req.Site != nil {
		siteDomain, sitePage = req.Site.Domain, req.Site.Page
	}
	var bannerWidth, bannerHeight int
	if banner.Banner != nil {
		bannerWidth, bannerHeight = banner.Banner.Width, banner.Banner.Height
	}
	decisionStarted := time.Now()
	result, err := s.decisioner.Decide(r.Context(), decision.Request{
		ID:              req.ID,
		ImpressionCount: len(req.Imp),
		SiteDomain:      siteDomain,
		SitePage:        sitePage,
		BannerWidth:     bannerWidth,
		BannerHeight:    bannerHeight,
		Currencies:      req.Cur,
	})
	decisionErr := err
	if decisionErr == nil && r.Context().Err() == context.DeadlineExceeded {
		decisionErr = context.DeadlineExceeded
	}
	s.metrics.ObserveDecision(result.Candidate, err != nil || result.NoBid, decisionErr, time.Since(decisionStarted))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "nbr": 2})
		return
	}
	// Standard no-bid response (nbr: 2 = technical/no candidate).
	writeJSON(w, http.StatusOK, map[string]any{
		"id":  result.RequestID,
		"nbr": result.NoBidReason,
	})
}

func (s *Server) handleWinNotice(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLossNotice(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleBillableNotice(w http.ResponseWriter, r *http.Request) {
	// Billable notice returns 204 No Content upon successful ledger transaction
	w.WriteHeader(http.StatusNoContent)
}

// Start runs the HTTP server.
func (s *Server) Start() error {
	s.logger.Info("starting DSP HTTP server", slog.String("addr", s.server.Addr))
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down DSP HTTP server")
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
		if r.URL.Path != "/openrtb2/bid" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.BidTimeout)
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
