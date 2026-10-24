package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/config"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/observability"
)

// Server wraps the standard library http.Server for DSP.
type Server struct {
	server  *http.Server
	logger  *slog.Logger
	cfg     *config.Config
	metrics *observability.Metrics
	isReady atomic.Bool
}

// NewServer initializes the HTTP server with router, middlewares, and probes.
func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	s := &Server{
		logger:  logger,
		cfg:     cfg,
		metrics: &observability.Metrics{},
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

func (s *Server) handleOpenRTBBid(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// Standard no-bid response (nbr: 2 = technical/no candidate)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":  reqID,
		"nbr": 2,
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
	s.isReady.Store(true)
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
