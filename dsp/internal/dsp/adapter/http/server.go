package http

import (
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

	"github.com/songfei1983/open-ads/dsp/internal/dsp/assets"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/config"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/decision"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/lease"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/observability"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

// SettlementStore abstracts the persistent settlement repository.
type SettlementStore interface {
	SettleReservation(ctx context.Context, params lease.SettlementParams) error
}

// Server wraps the standard library http.Server for DSP.
type Server struct {
	server          *http.Server
	logger          *slog.Logger
	cfg             *config.Config
	metrics         *observability.Metrics
	decisioner      decision.Decisioner
	adminHandler    http.Handler
	reserver        lease.Reserver
	tokenCodec      *lease.TokenCodec
	assetStore      *assets.FileStore
	settlementStore SettlementStore
	isReady         atomic.Bool
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

// Metrics returns the observability metrics instance.
func (s *Server) Metrics() *observability.Metrics {
	return s.metrics
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /metrics", s.metrics.Handler)
	mux.HandleFunc("POST /openrtb2/bid", s.handleOpenRTBBid)
	mux.HandleFunc("GET /rtb/v1/win/{bid_id}", s.handleWinNotice)
	mux.HandleFunc("POST /rtb/v1/win/{bid_id}", s.handleWinNotice)
	mux.HandleFunc("GET /rtb/v1/loss/{bid_id}", s.handleLossNotice)
	mux.HandleFunc("POST /rtb/v1/loss/{bid_id}", s.handleLossNotice)
	mux.HandleFunc("POST /rtb/v1/billable/{bid_id}", s.handleBillableNotice)
	mux.HandleFunc("GET /render/v1/{creative_id}", s.handleRender)
	// UI prototype routes – the handler resolves the prototype directory
	// itself, injects the live-reload watcher into index.html and exposes
	// the /ui/revision fingerprint endpoint the watcher polls.
	mux.Handle("/ui/", UIHandler())
	// Redirect the root path to the UI landing page for easier local testing.
	// Simple root redirect – only triggers on an exact "/" request.
	// Using a method‑agnostic pattern avoids conflicts with the admin API routes.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			// Defer to other handlers for any non‑root path.
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
	})
	// Simple unauthenticated demo endpoint for UI prototypes to fetch a list of campaigns.
	// Returns a static JSON payload sufficient for UI rendering during sandbox demos.
	mux.HandleFunc("GET /ui/api/campaigns", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"cmp_demo","name":"Demo Campaign","advertiser":"Acme","period":"2026-10-01/2026-12-31","status":"active"}]`))
	})
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

// SetReserver configures the budget reserver used to settle reservations.
func (s *Server) SetReserver(reserver lease.Reserver) {
	s.reserver = reserver
}

// SetTokenCodec configures the AEAD codec used to seal/open reservation references.
func (s *Server) SetTokenCodec(codec *lease.TokenCodec) {
	s.tokenCodec = codec
}

// SetAssetStore attaches the immutable asset store used to serve creative renders.
func (s *Server) SetAssetStore(store *assets.FileStore) {
	s.assetStore = store
}

// SetSettlementStore configures the persistent settlement repository.
func (s *Server) SetSettlementStore(store SettlementStore) {
	s.settlementStore = store
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
	if err != nil || result.NoBid {
		nbr := result.NoBidReason
		if nbr == 0 {
			nbr = 2
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":  req.ID,
			"nbr": nbr,
		})
		return
	}

	impID := "1"
	if len(req.Imp) > 0 {
		var impHeader struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Imp[0], &impHeader); err == nil && impHeader.ID != "" {
			impID = impHeader.ID
		}
	} else if result.ImpressionID != "" {
		impID = result.ImpressionID
	}
	currency := result.Currency
	if currency == "" {
		currency = "USD"
	}
	ext := map[string]any{
		"reservation_id": result.ReservationID,
	}
	if result.ReservationToken != "" {
		ext["reservation_ref"] = result.ReservationToken
	}

	bidResponse := map[string]any{
		"id": req.ID,
		"seatbid": []map[string]any{
			{
				"bid": []map[string]any{
					{
						"id":    result.BidID,
						"impid": impID,
						"price": result.Price,
						"adid":  result.CandidateLineItemID,
						"nurl":  result.WinNoticeURL,
						"burl":  result.BillableNoticeURL,
						"lurl":  result.LossNoticeURL,
						"adm":   result.AdMarkup,
						"cid":   result.CampaignID,
						"crid":  result.CreativeID,
						"w":     result.Width,
						"h":     result.Height,
						"ext":   ext,
					},
				},
				"seat": "dsp-seat-acme",
			},
		},
		"bidid": result.BidID,
		"cur":   currency,
	}
	writeJSON(w, http.StatusOK, bidResponse)
}

// handleRender serves the underlying asset of an active published creative.
// The identity check always goes through the in-process snapshot and the
// content-addressed store, never through request-controlled claims.
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	creativeID := r.PathValue("creative_id")
	if domain.ValidateUUID32(creativeID) != nil || creativeID != strings.ToLower(creativeID) {
		http.NotFound(w, r)
		return
	}
	manifest, _, err := snapshot.LoadCurrent()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	creative, ok := manifest.Creatives[creativeID]
	if !ok || !creative.Active {
		http.NotFound(w, r)
		return
	}
	asset := creative.AssetMeta
	if asset.Hash == "" || !strings.Contains(asset.MIMEType, "image/") {
		http.NotFound(w, r)
		return
	}
	if s.assetStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code": "asset_store_unavailable", "message": "creative rendering is not configured",
		})
		return
	}
	tenant := strings.ToLower(strings.ReplaceAll(s.cfg.AdminTenantID, "-", ""))
	if tenant == "" {
		http.NotFound(w, r)
		return
	}
	reader, size, err := s.assetStore.Open(r.Context(), tenant, strings.ToLower(asset.Hash))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", strings.ToLower(asset.MIMEType))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Content-addressed objects are immutable; allow aggressive caching.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if size >= 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}
	if _, err := io.Copy(w, reader); err != nil {
		// Headers are already committed; the truncated body signals failure.
		s.logger.Warn("failed to stream rendered asset", slog.String("creative", creativeID), slog.Any("error", err))
	}
}

func (s *Server) handleWinNotice(w http.ResponseWriter, r *http.Request) {
	// Win notice (nurl) records win status without altering held budget or spend
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLossNotice(w http.ResponseWriter, r *http.Request) {
	bidID := r.PathValue("bid_id")
	refToken := r.URL.Query().Get("ref")
	if r.Body != nil {
		var reqBody struct {
			AuctionID      string `json:"auction_id"`
			BidID          string `json:"bid_id"`
			LossReason     int    `json:"loss_reason"`
			LossReasonText string `json:"loss_reason_text"`
			ReservationRef string `json:"reservation_ref"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&reqBody)
		if reqBody.ReservationRef != "" {
			refToken = reqBody.ReservationRef
		}
		if reqBody.BidID != "" {
			bidID = reqBody.BidID
		}
	}

	var claims lease.ReservationClaims
	var err error
	if s.tokenCodec != nil && refToken != "" {
		claims, err = s.tokenCodec.Open(refToken, time.Now().UTC())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code": "invalid_token", "message": "invalid or expired reservation token",
			})
			return
		}
	}

	resID := claims.ReservationID
	if resID == "" {
		resID = bidID
	}

	if s.settlementStore != nil && claims.ReservationID != "" {
		params := lease.SettlementParams{
			TenantID:       claims.TenantID,
			ReservationID:  claims.ReservationID,
			Outcome:        lease.OutcomeLoss,
			ActualAmount:   0,
			Currency:       claims.Currency,
			SourceEventKey: "loss:" + bidID,
			Demands:        claims.Demands,
			LeaseIDs:       claims.LeaseIDs,
			BidID:          bidID,
			AuctionID:      claims.AuctionID,
			ImpID:          claims.ImpressionID,
		}
		if err := s.settlementStore.SettleReservation(r.Context(), params); err != nil {
			if errors.Is(err, lease.ErrReservationConflict) {
				writeJSON(w, http.StatusConflict, map[string]string{
					"code": "reservation_conflict", "message": err.Error(),
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"code": "settlement_failed", "message": err.Error(),
			})
			return
		}
	}
	if s.reserver != nil && resID != "" {
		_, _ = s.reserver.Settle(resID, lease.OutcomeLoss, 0, time.Now().UTC())
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleBillableNotice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventID        string `json:"event_id"`
		SourceEventKey string `json:"source_event_key"`
		AuctionID      string `json:"auction_id"`
		BidID          string `json:"bid_id"`
		ImpID          string `json:"imp_id"`
		ReservationRef string `json:"reservation_ref"`
		AmountMicros   int64  `json:"amount_micros"`
		Currency       string `json:"currency"`
		OccurredAt     string `json:"occurred_at"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code": "invalid_request", "message": "invalid billable notice body or missing event_id",
		})
		return
	}
	bidID := r.PathValue("bid_id")
	if bidID == "" {
		bidID = req.BidID
	}

	var claims lease.ReservationClaims
	if s.tokenCodec != nil && req.ReservationRef != "" {
		var err error
		claims, err = s.tokenCodec.Open(req.ReservationRef, time.Now().UTC())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code": "invalid_token", "message": "invalid or expired reservation token",
			})
			return
		}
		if claims.BidID != "" && bidID != "" && claims.BidID != bidID {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code": "invalid_request", "message": "bid_id does not match reservation token",
			})
			return
		}
		if req.AmountMicros > claims.ReservedMicros {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code": "invalid_amount", "message": "billable amount exceeds reserved maximum risk",
			})
			return
		}
		if req.Currency != "" && claims.Currency != "" && req.Currency != claims.Currency {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code": "currency_mismatch", "message": "currency does not match reservation",
			})
			return
		}
	}

	resID := claims.ReservationID
	if resID == "" {
		resID = req.ReservationRef
		if resID == "" {
			resID = bidID
		}
	}

	occurredAt := time.Now().UTC()
	if req.OccurredAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, req.OccurredAt); err == nil {
			occurredAt = parsed
		} else if parsed, err := time.Parse(time.RFC3339, req.OccurredAt); err == nil {
			occurredAt = parsed
		}
	}
	sourceKey := req.SourceEventKey
	if sourceKey == "" {
		sourceKey = req.EventID
	}

	if s.settlementStore != nil && claims.ReservationID != "" {
		params := lease.SettlementParams{
			TenantID:       claims.TenantID,
			ReservationID:  claims.ReservationID,
			Outcome:        lease.OutcomeBillable,
			ActualAmount:   domain.Micros(req.AmountMicros),
			Currency:       req.Currency,
			SourceEventKey: sourceKey,
			Demands:        claims.Demands,
			LeaseIDs:       claims.LeaseIDs,
			EventID:        req.EventID,
			AuctionID:      req.AuctionID,
			BidID:          bidID,
			ImpID:          req.ImpID,
			OccurredAt:     occurredAt,
		}
		if err := s.settlementStore.SettleReservation(r.Context(), params); err != nil {
			if errors.Is(err, lease.ErrReservationConflict) {
				writeJSON(w, http.StatusConflict, map[string]string{
					"code": "reservation_conflict", "message": err.Error(),
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"code": "settlement_failed", "message": err.Error(),
			})
			return
		}
	}

	if s.reserver != nil && resID != "" {
		_, err := s.reserver.Settle(resID, lease.OutcomeBillable, domain.Micros(req.AmountMicros), time.Now().UTC())
		if err != nil && errors.Is(err, lease.ErrReservationConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"code": "reservation_conflict", "message": err.Error(),
			})
			return
		}
	}

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
