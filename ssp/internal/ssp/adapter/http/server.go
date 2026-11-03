package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/auction"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/config"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/dmp"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/limiter"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/observability"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/render"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

// Server wraps the standard library http.Server for the SSP.
type Server struct {
	server          *http.Server
	logger          *slog.Logger
	cfg             *config.Config
	metrics         *observability.Metrics
	engine          *auction.Engine
	codec           *token.Codec
	tracker         *render.Tracker
	renderer        *render.Renderer
	dmp             *dmp.Adapter
	adminHandler    http.Handler
	internalHandler http.Handler
	limiter         *limiter.Limiter
	isReady         atomic.Bool
}

// NewServer initializes the HTTP server with routing, middleware and probes.
func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	s := &Server{
		logger:  logger,
		cfg:     cfg,
		metrics: &observability.Metrics{},
		limiter: limiter.New(),
	}
	s.engine = auction.NewEngine(s.metrics, logger)

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := s.requestIDMiddleware(s.ingressDeadlineMiddleware(s.loggingMiddleware(s.recoveryMiddleware(mux))))

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

// SetReady updates the readiness state.
func (s *Server) SetReady(ready bool) { s.isReady.Store(ready) }

// SetTokenCodec installs the AEAD codec used for render, click and event tokens.
func (s *Server) SetTokenCodec(codec *token.Codec) {
	s.codec = codec
	s.tracker = &render.Tracker{Codec: codec, TenantID: s.cfg.AdminTenantID}
	if s.renderer != nil {
		s.renderer.Codec = codec
	}
}

// SetRenderer installs the read-only creative renderer.
func (s *Server) SetRenderer(renderer *render.Renderer) { s.renderer = renderer }

// SetDMPAdapter installs the audience resolver.
func (s *Server) SetDMPAdapter(adapter *dmp.Adapter) { s.dmp = adapter }

// SetAdminHandler installs the tenant-authenticated management API.
func (s *Server) SetAdminHandler(handler http.Handler) { s.adminHandler = handler }

// SetInternalHandler installs the DSP-facing internal API.
func (s *Server) SetInternalHandler(handler http.Handler) { s.internalHandler = handler }

// Handler returns the wrapped handler for tests.
func (s *Server) Handler() http.Handler { return s.server.Handler }

// Metrics returns the observability instance.
func (s *Server) Metrics() *observability.Metrics { return s.metrics }

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /metrics", s.metrics.Handler)
	mux.HandleFunc("POST /openrtb2/auction", s.handleOpenRTBAuction)
	mux.HandleFunc("/sdk/v1/ad-requests", s.handleAdRequests)
	mux.HandleFunc("POST /sdk/v1/impressions", s.handleImpression)
	mux.HandleFunc("GET /render/v1/assets/{token}", s.handleRenderAsset)
	mux.HandleFunc("GET /render/v1/{token}", s.handleRender)
	mux.HandleFunc("GET /click/v1/{token}", s.handleClick)
	mux.HandleFunc("GET /sdk/v1/openads-sdk.js", s.handleSDK)
	mux.Handle("/admin/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminHandler == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "admin_unavailable", "message": "management API is not configured"})
			return
		}
		s.adminHandler.ServeHTTP(w, r)
	}))
	mux.Handle("/internal/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.internalHandler == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "internal_unavailable", "message": "internal API is not configured"})
			return
		}
		s.internalHandler.ServeHTTP(w, r)
	}))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": s.cfg.ServiceName})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	version := snapshot.CurrentVersion()
	if !s.isReady.Load() || version == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready", "service": s.cfg.ServiceName, "config_version": version,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ready", "service": s.cfg.ServiceName, "config_version": version,
	})
}

type supplyRequest struct {
	ID  string `json:"id"`
	Imp []struct {
		ID     string `json:"id"`
		TagID  string `json:"tagid"`
		Banner struct {
			W int `json:"w"`
			H int `json:"h"`
		} `json:"banner"`
	} `json:"imp"`
	Site *struct {
		Domain string `json:"domain"`
		Page   string `json:"page"`
	} `json:"site"`
	TMax int      `json:"tmax"`
	Cur  []string `json:"cur"`
}

func (s *Server) handleOpenRTBAuction(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	manifest, _, err := snapshot.Current()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "config_unavailable", "no published SSP configuration", reqID)
		return
	}
	credential := strings.TrimSpace(r.Header.Get("X-Supply-Key"))
	if credential == "" {
		credential = bearerToken(r)
	}
	if credential == "" {
		writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "supply credential required", reqID)
		return
	}
	endpoint, ok := manifest.SupplyByCredentialHash(snapshot.HashSHA256Hex(credential))
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "supply credential is not valid", reqID)
		return
	}
	if !s.limiter.Allow("supply:"+endpoint.ID, endpoint.RateLimitPerSecond) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "supply request rate exceeded", reqID)
		return
	}
	property, ok := manifest.Properties[endpoint.PropertyID]
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "config_unavailable", "supply endpoint has no property", reqID)
		return
	}
	var request supplyRequest
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil || request.ID == "" || len(request.ID) > 128 || len(request.Imp) != 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "a single valid OpenRTB impression is required", reqID)
		return
	}
	imp := request.Imp[0]
	placement, ok := manifest.Supports(imp.TagID)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "tagid does not identify an active placement", reqID)
		return
	}
	if placement.PropertyID != endpoint.PropertyID {
		writeAPIError(w, http.StatusForbidden, "supply_mismatch", "placement is not authorized for this supply endpoint", reqID)
		return
	}
	if imp.Banner.W != placement.Width || imp.Banner.H != placement.Height {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "impression size does not match the placement", reqID)
		return
	}
	var siteDomain, sitePage string
	if request.Site != nil {
		siteDomain, sitePage = request.Site.Domain, request.Site.Page
	}
	if property.ExternalID != "" {
		expected := normalizeDomain(property.ExternalID)
		if siteDomain == "" && sitePage != "" {
			if parsed, parseErr := url.Parse(sitePage); parseErr == nil {
				siteDomain = parsed.Hostname()
			}
		}
		if normalizeDomain(siteDomain) != expected {
			writeAPIError(w, http.StatusForbidden, "supply_mismatch", "site does not match the supply profile", reqID)
			return
		}
	}
	// Server-to-server Supply calls carry no Origin; browsers must match the
	// endpoint allowlist exactly when one is configured.
	if origin := r.Header.Get("Origin"); origin != "" && !snapshot.OriginAllowed(endpoint.AllowedOrigins, origin) {
		writeAPIError(w, http.StatusForbidden, "origin_not_allowed", "origin is not allowlisted", reqID)
		return
	}
	if len(request.Cur) > 0 && !currencyAllowed(request.Cur, placement.Currency) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "requested currency is not supported", reqID)
		return
	}
	auctionID, err := domain.NewUUIDv7()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to create auction", reqID)
		return
	}
	budget := s.cfg.AuctionTimeout
	if request.TMax > 0 {
		candidate := time.Duration(request.TMax) * time.Millisecond
		if candidate < budget {
			budget = candidate
		}
	}
	started := time.Now()
	outcome := s.engine.Run(r.Context(), manifest, auction.Request{
		AuctionID: auctionID, PlacementID: placement.ID, SupplyEndpointID: endpoint.ID,
		SiteDomain: normalizeDomain(siteDomain), SitePage: sitePage,
		Width: placement.Width, Height: placement.Height, Currency: placement.Currency,
		FloorCPMMicros: placement.FloorCPMMicros, TMax: budget,
	})
	if !outcome.Filled {
		writeJSON(w, http.StatusOK, map[string]any{"id": request.ID, "nbr": 2})
		s.logAuction(reqID, auctionID, placement.ID, "no_fill", started)
		return
	}
	ad, err := s.mintAd(r, placement, outcome)
	if err != nil {
		s.logger.Error("failed to mint supply render tokens", slog.String("request_id", reqID), slog.Any("error", err))
		writeJSON(w, http.StatusOK, map[string]any{"id": request.ID, "nbr": 2})
		return
	}
	go s.engine.Notify(context.Background(), outcome)
	adm := fmt.Sprintf("<iframe src=\"%s\" width=\"%d\" height=\"%d\" frameborder=\"0\" scrolling=\"no\" sandbox=\"allow-scripts allow-popups allow-popups-to-escape-sandbox\"></iframe>",
		htmlEscape(ad.RenderURL), placement.Width, placement.Height)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    request.ID,
		"cur":   placement.Currency,
		"bidid": outcome.Winner.BidID,
		"seatbid": []map[string]any{{
			"bid": []map[string]any{{
				"id": outcome.Winner.BidID, "impid": "1", "price": json.Number(auction.FormatCPMMicros(outcome.Winner.CPMMicros)),
				"crid": outcome.Winner.CreativeID, "cid": outcome.Winner.CampaignID,
				"w": placement.Width, "h": placement.Height, "adm": adm,
			}},
		}},
	})
	s.logAuction(reqID, auctionID, placement.ID, "filled", started)
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

func (s *Server) handleAdRequests(w http.ResponseWriter, r *http.Request) {
	reqID := observability.RequestIDFromContext(r.Context())
	if r.Method == http.MethodOptions {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		w.Header().Set("Access-Control-Max-Age", "60")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed", reqID)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	manifest, _, err := snapshot.Current()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "config_unavailable", "no published SSP configuration", reqID)
		return
	}
	var request adRequest
	if !decodeJSON(w, r, &request, 64<<10) {
		return
	}
	if domain.ValidateUUID32(request.RequestID) == nil {
		reqID = request.RequestID
		w.Header().Set("X-Request-ID", reqID)
	}
	if request.PlacementID == "" || request.Slot.Width <= 0 || request.Slot.Height <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "placement and slot are required", reqID)
		return
	}
	placement, ok := manifest.Supports(request.PlacementID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "placement_not_found", "placement is not active", reqID)
		return
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = request.Page.Origin
	}
	if !snapshot.OriginAllowed(placement.AllowedOrigins, origin) {
		writeAPIError(w, http.StatusForbidden, "origin_not_allowed", "origin is not allowlisted", reqID)
		return
	}
	if request.Slot.Width != placement.Width || request.Slot.Height != placement.Height {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "slot size does not match the placement", reqID)
		return
	}
	if s.cfg.ClientRateLimit > 0 && !s.limiter.Allow("client:"+placement.ID, s.cfg.ClientRateLimit) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "client request rate exceeded", reqID)
		return
	}
	responseOrigin := origin
	if responseOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", responseOrigin)
		w.Header().Set("Vary", "Origin")
	}
	started := time.Now()
	segments, resolveErr := s.resolveAudience(r.Context(), reqID, placement, request)
	if resolveErr != nil {
		s.logger.Warn("audience resolve failed; failing closed", slog.String("request_id", reqID), slog.Any("error", resolveErr))
		writeNoFill(w, reqID, "no_fill")
		s.logAuction(reqID, "", placement.ID, "no_fill_audience", started)
		return
	}
	auctionID, err := domain.NewUUIDv7()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to create auction", reqID)
		return
	}
	budget := s.cfg.AuctionTimeout
	outcome := s.engine.Run(r.Context(), manifest, auction.Request{
		AuctionID: auctionID, PlacementID: placement.ID, SiteDomain: normalizeDomain(request.Page.Origin),
		SitePage: request.Page.Origin + request.Page.Path, Width: placement.Width, Height: placement.Height,
		Currency: placement.Currency, FloorCPMMicros: placement.FloorCPMMicros, TMax: budget,
		Segments: segments,
	})
	if !outcome.Filled {
		writeNoFill(w, reqID, "no_fill")
		s.logAuction(reqID, auctionID, placement.ID, "no_fill", started)
		return
	}
	ad, err := s.mintAd(r, placement, outcome)
	if err != nil {
		s.logger.Error("failed to mint SDK render tokens", slog.String("request_id", reqID), slog.Any("error", err))
		writeNoFill(w, reqID, "no_fill")
		return
	}
	go s.engine.Notify(context.Background(), outcome)
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": reqID,
		"status":     "filled",
		"ad": map[string]any{
			"creative_id": outcome.Winner.CreativeID,
			"format":      "image",
			"render_url":  ad.RenderURL,
			"click_url":   ad.ClickURL,
			"event_token": ad.EventToken,
			"expires_at":  ad.ExpiresAt,
		},
		"diagnostics": map[string]any{
			"auction_id":  auctionID,
			"outcome":     "bid",
			"duration_ms": time.Since(started).Milliseconds(),
		},
	})
	s.logAuction(reqID, auctionID, placement.ID, "filled", started)
}

type adTokens struct {
	RenderURL  string
	ClickURL   string
	EventToken string
	ExpiresAt  string
}

func (s *Server) mintAd(r *http.Request, placement snapshot.Placement, outcome auction.Outcome) (adTokens, error) {
	if s.codec == nil {
		return adTokens{}, errors.New("renderer token codec is not configured")
	}
	now := time.Now().UTC()
	expires := now.Add(s.cfg.RenderTokenTTL)
	eventExpires := now.Add(s.cfg.EventTokenTTL)
	winner := outcome.Winner
	eventToken, err := s.codec.Seal(token.AudienceEvent, token.EventClaims{
		TenantID: s.cfg.AdminTenantID, PlacementID: placement.ID, AuctionID: outcome.AuctionID,
		BidID: winner.BidID, ImpID: winner.ImpID, CreativeID: winner.CreativeID,
		DSPEndpointID: winner.EndpointID, Reservation: winner.Reservation,
		BidCPMMicros: winner.CPMMicros, Currency: winner.Currency, BURLTarget: winner.BURL,
		ExpiresAt: eventExpires,
	})
	if err != nil {
		return adTokens{}, err
	}
	clickToken, err := s.codec.Seal(token.AudienceClick, token.ClickClaims{
		TenantID: s.cfg.AdminTenantID, AuctionID: outcome.AuctionID, BidID: winner.BidID,
		LandingURL: winner.Creative.LandingURL, ExpiresAt: expires,
	})
	if err != nil {
		return adTokens{}, err
	}
	frameAncestors := strings.Join(placement.AllowedOrigins, " ")
	renderToken, err := s.codec.Seal(token.AudienceRender, token.RenderClaims{
		TenantID: s.cfg.AdminTenantID, PlacementID: placement.ID, AuctionID: outcome.AuctionID,
		BidID: winner.BidID, ImpID: winner.ImpID, CreativeID: winner.CreativeID,
		DSPEndpointID: winner.EndpointID, AssetRef: winner.Creative.AssetRef,
		AssetSHA256: winner.Creative.AssetSHA256, MIMEType: winner.Creative.MIMEType,
		Width: winner.Creative.Width, Height: winner.Creative.Height, LandingURL: winner.Creative.LandingURL,
		FrameAncestor: frameAncestors, ClickToken: clickToken, EventToken: eventToken, ExpiresAt: expires,
	})
	if err != nil {
		return adTokens{}, err
	}
	origin := requestOrigin(r)
	return adTokens{
		RenderURL:  origin + "/render/v1/" + renderToken,
		ClickURL:   origin + "/click/v1/" + clickToken,
		EventToken: eventToken,
		ExpiresAt:  expires.Format(time.RFC3339),
	}, nil
}

func (s *Server) resolveAudience(ctx context.Context, reqID string, placement snapshot.Placement, request adRequest) ([]dmp.Segment, error) {
	if placement.PrivacyPolicy == "no_identity" {
		return nil, nil
	}
	if strings.TrimSpace(request.Consent.Value) == "" {
		return nil, nil
	}
	if s.dmp == nil || !s.dmp.Enabled() {
		return nil, nil
	}
	segments, err := s.dmp.Resolve(ctx, reqID, request.Consent.Value, s.cfg.MaxSegments)
	if err != nil {
		return nil, err
	}
	s.metrics.ObserveAudienceResolve()
	return segments, nil
}

func (s *Server) handleImpression(w http.ResponseWriter, r *http.Request) {
	if s.tracker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "rejected"})
		return
	}
	s.tracker.Handle(w, r)
}

func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	if s.renderer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "renderer_unavailable"})
		return
	}
	s.renderer.Render(w, r, r.PathValue("token"))
}

func (s *Server) handleRenderAsset(w http.ResponseWriter, r *http.Request) {
	if s.renderer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "renderer_unavailable"})
		return
	}
	s.renderer.Asset(w, r, r.PathValue("token"))
}

func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
	if s.renderer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "renderer_unavailable"})
		return
	}
	s.renderer.Click(w, r, r.PathValue("token"))
}

func (s *Server) logAuction(reqID, auctionID, placementID, outcome string, started time.Time) {
	s.logger.Info("auction completed",
		slog.String("request_id", reqID),
		slog.String("auction_id", auctionID),
		slog.String("placement_id", placementID),
		slog.String("outcome", outcome),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
	)
}

// Start runs the HTTP server.
func (s *Server) Start() error {
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

func (s *Server) ingressDeadlineMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/openrtb2/") && !strings.HasPrefix(r.URL.Path, "/sdk/v1/ad-requests") {
			next.ServeHTTP(w, r)
			return
		}
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
		s.logger.Info("http request",
			slog.String("method", r.Method),
			slog.String("route", safeRoute(r.URL.Path)),
			slog.Int("status", rw.statusCode),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("request_id", observability.RequestIDFromContext(r.Context())),
		)
		s.metrics.Observe(rw.statusCode, time.Since(start))
	})
}

func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				reqID := observability.RequestIDFromContext(r.Context())
				s.logger.Error("panic recovered", slog.Any("recover", rec), slog.String("request_id", reqID))
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"code": "internal_error", "message": "An unexpected error occurred.", "request_id": reqID,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// safeRoute keeps opaque bearer tokens out of access logs.
func safeRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/render/v1/assets/"):
		return "/render/v1/assets/{token}"
	case strings.HasPrefix(path, "/render/v1/"):
		return "/render/v1/{token}"
	case strings.HasPrefix(path, "/click/v1/"):
		return "/click/v1/{token}"
	default:
		return path
	}
}

func requestOrigin(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}

func bearerToken(r *http.Request) string {
	authorization := r.Header.Get("Authorization")
	if strings.HasPrefix(authorization, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	}
	return ""
}

func normalizeDomain(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
		value = parsed.Hostname()
	}
	return strings.TrimSuffix(strings.ToLower(value), ".")
}

func currencyAllowed(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), expected) {
			return true
		}
	}
	return false
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "\"", "&quot;", "'", "&#39;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

func writeNoFill(w http.ResponseWriter, reqID, outcome string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":  reqID,
		"status":      "no_fill",
		"diagnostics": map[string]any{"outcome": outcome},
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	reqID := observability.RequestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid request body", reqID)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value", reqID)
		return false
	}
	return true
}

func writeAPIError(w http.ResponseWriter, status int, code, message, reqID string) {
	payload := map[string]string{"code": code, "message": message}
	if reqID != "" {
		payload["request_id"] = reqID
	}
	writeJSON(w, status, payload)
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
