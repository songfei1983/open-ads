package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/control"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/domain"
	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

// adminRequestTimeout bounds management API requests, which write to ClickHouse
// and therefore cannot use the bid-path deadline.
const adminRequestTimeout = 10 * time.Second

// Server wraps the standard library http.Server for DMP.
type Server struct {
	server  *http.Server
	logger  *slog.Logger
	cfg     *config.Config
	metrics *observability.Metrics
	isReady atomic.Bool
	control control.Store
}

// Option customizes the server at construction time.
type Option func(*Server)

// WithControlStore attaches the tenant-scoped definition store that backs the
// management API. Without it the admin surface answers 503.
func WithControlStore(store control.Store) Option {
	return func(s *Server) { s.control = store }
}

// NewServer initializes the HTTP server with router, middlewares, and probes.
func NewServer(cfg *config.Config, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		logger:  logger,
		cfg:     cfg,
		metrics: &observability.Metrics{},
	}
	for _, opt := range opts {
		opt(s)
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
	mux.HandleFunc("POST /internal/v1/audiences:resolve", s.handleAudienceResolve)
	mux.HandleFunc("POST /internal/v1/identity:revoke", s.handleIdentityRevoke)

	controlHandler := control.NewHandler(control.HandlerOptions{
		Store:  s.control,
		Token:  s.cfg.AdminToken,
		Actor:  s.cfg.AdminActor,
		Logger: s.logger,
	})
	mux.Handle("/admin/v1/", controlHandler)
	mux.Handle("/admin/v1", controlHandler)
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

// audienceResolveMessage mirrors the internal resolve API request; only
// server-declared fields are accepted (fail-closed decoding).
type audienceResolveMessage struct {
	RequestID    string   `json:"request_id"`
	TenantID     string   `json:"tenant_id"`
	ProviderID   string   `json:"provider_id"`
	Namespace    string   `json:"namespace"`
	SubjectToken string   `json:"subject_token"`
	Purposes     []string `json:"purposes"`
	MaxSegments  int      `json:"max_segments"`
}

// sandboxResolvedSegment is a documented demo-only interest segment returned
// for the well-known sandbox consent token. Production resolution must come
// from membership snapshots, never from this stub path.
type sandboxResolvedSegment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sandboxFixtureSubjectToken is the token the console UI sends when the user
// opts in; other tokens resolve no segments.
const sandboxFixtureSubjectToken = "sandbox-consent-demo-01"

func (s *Server) handleAudienceResolve(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	var req audienceResolveMessage
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_request", "message": "invalid audience resolve request", "request_id": reqID})
		return
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_request", "message": "invalid audience resolve request", "request_id": reqID})
		return
	}

	// Audit without ever persisting the raw subject token.
	digest := sha256.Sum256([]byte(req.SubjectToken))
	subjectHash := hex.EncodeToString(digest[:8])
	segments := []sandboxResolvedSegment{}
	if req.SubjectToken == sandboxFixtureSubjectToken {
		segments = []sandboxResolvedSegment{
			{ID: "seg-demo-sports", Name: "演示 · 运动爱好者"},
			{ID: "seg-demo-tech", Name: "演示 · 数码科技"},
		}
	}
	if req.MaxSegments > 0 && len(segments) > req.MaxSegments {
		segments = segments[:req.MaxSegments]
	}
	s.logger.Info("audience resolve", slog.String("request_id", reqID),
		slog.String("subject_hash", subjectHash), slog.Int("segments", len(segments)))

	// Standard resolve response matching API.md
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":  reqID,
		"snapshot_id": "snap-sandbox-fixture",
		"provider_id": req.ProviderID,
		"segments":    segments,
		"resolved_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleIdentityRevoke(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())

	// Never report completion until the persistent tombstone and deny gate exist.
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"code":       "not_implemented",
		"message":    "identity revocation is not available",
		"request_id": reqID,
	})
}

// Start runs the HTTP server.
func (s *Server) Start() error {
	s.isReady.Store(true)
	s.logger.Info("starting DMP HTTP server", slog.String("addr", s.server.Addr))
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down DMP HTTP server")
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
		// The bid-path deadline is far too tight for ClickHouse writes on the
		// management API, so admin requests get their own budget.
		timeout := s.cfg.RequestTimeout
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			timeout = adminRequestTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
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
