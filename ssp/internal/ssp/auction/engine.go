// Package auction orchestrates the SSP fan-out, bid validation and first-price
// winner selection. It is transport neutral apart from the OpenRTB wire types.
package auction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/dmp"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/observability"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

// OpenRTB loss reason codes used by the sandbox profile.
const (
	LossBelowFloor         = 100
	LossExchangeFilter     = 101
	LossHigherBid          = 102
	LossOpportunityExpired = 2
)

const (
	maxBidResponseBytes = 1 << 20
	minRenderBudget     = time.Millisecond
)

// Request is the transport-neutral auction input.
type Request struct {
	AuctionID        string
	PlacementID      string
	SupplyEndpointID string
	SiteDomain       string
	SitePage         string
	Width            int
	Height           int
	Currency         string
	FloorCPMMicros   int64
	TMax             time.Duration
	Segments         []dmp.Segment
}

// Bid is a validated candidate.
type Bid struct {
	EndpointID   string
	EndpointName string
	Priority     int
	BidID        string
	ImpID        string
	CampaignID   string
	CreativeID   string
	CPMMicros    int64
	Currency     string
	Reservation  string
	BURL         string
	NURL         string
	LURL         string
	Creative     snapshot.Creative
}

// Notice is one deferred win or loss callback.
type Notice struct {
	URL         string
	Reason      int
	PriceMicros int64
	NoticeID    string
}

// Outcome is the complete auction result.
type Outcome struct {
	AuctionID  string
	Filled     bool
	Winner     Bid
	Losers     []Bid
	Rejections []string
	Duration   time.Duration
	Notices    []Notice
}

// Engine runs auctions against a manifest.
type Engine struct {
	Client        *http.Client
	NoticeClient  *http.Client
	NoticeTimeout time.Duration
	Metrics       *observability.Metrics
	Logger        *slog.Logger
	Now           func() time.Time
}

// NewEngine builds an engine with bounded, non-redirecting HTTP clients.
func NewEngine(metrics *observability.Metrics, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Engine{
		Client:        client,
		NoticeClient:  &http.Client{Transport: transport, Timeout: 500 * time.Millisecond},
		NoticeTimeout: 300 * time.Millisecond,
		Metrics:       metrics,
		Logger:        logger,
		Now:           func() time.Time { return time.Now().UTC() },
	}
}

type wireRequest struct {
	ID   string    `json:"id"`
	AT   int       `json:"at"`
	TMax int       `json:"tmax,omitempty"`
	Cur  []string  `json:"cur,omitempty"`
	Imp  []wireImp `json:"imp"`
	Site *wireSite `json:"site,omitempty"`
	User *wireUser `json:"user,omitempty"`
}

type wireImp struct {
	ID       string      `json:"id"`
	TagID    string      `json:"tagid,omitempty"`
	BidFloor json.Number `json:"bidfloor,omitempty"`
	Currency string      `json:"bidfloorcur,omitempty"`
	Banner   wireBanner  `json:"banner"`
}

type wireBanner struct {
	W int `json:"w"`
	H int `json:"h"`
}

type wireSite struct {
	Domain string `json:"domain,omitempty"`
	Page   string `json:"page,omitempty"`
}

type wireUser struct {
	Data []wireData `json:"data,omitempty"`
}

type wireData struct {
	ID      string        `json:"id"`
	Segment []wireSegment `json:"segment"`
}

type wireSegment struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type wireResponse struct {
	ID      string        `json:"id"`
	Cur     string        `json:"cur"`
	SeatBid []wireSeatBid `json:"seatbid"`
}

type wireSeatBid struct {
	Bid []wireBid `json:"bid"`
}

type wireBid struct {
	ID    string      `json:"id"`
	ImpID string      `json:"impid"`
	Price json.Number `json:"price"`
	AdID  string      `json:"adid,omitempty"`
	CID   string      `json:"cid,omitempty"`
	Crid  string      `json:"crid"`
	W     int         `json:"w,omitempty"`
	H     int         `json:"h,omitempty"`
	AdM   string      `json:"adm,omitempty"`
	NURL  string      `json:"nurl,omitempty"`
	LURL  string      `json:"lurl,omitempty"`
	BURL  string      `json:"burl,omitempty"`
	Ext   struct {
		ReservationRef string      `json:"reservation_ref"`
		OpenADS        *openADSExt `json:"openads"`
	} `json:"ext"`
}

type openADSExt struct {
	AssetRef      string `json:"asset_ref"`
	AssetSHA256   string `json:"asset_sha256"`
	AssetWidthPX  int    `json:"asset_width_px"`
	AssetHeightPX int    `json:"asset_height_px"`
	LandingURL    string `json:"landing_url"`
}

type bidderResult struct {
	endpoint snapshot.DSPEndpoint
	response wireResponse
	err      error
	outcome  string
}

// Run fans out to every active bidder and selects one winner.
func (e *Engine) Run(ctx context.Context, manifest *snapshot.Manifest, request Request) Outcome {
	started := e.now()
	outcome := Outcome{AuctionID: request.AuctionID}
	defer func() { outcome.Duration = e.now().Sub(started) }()
	if manifest == nil {
		outcome.Rejections = append(outcome.Rejections, "no_snapshot")
		e.observeAuction("no_fill")
		return outcome
	}
	placement, ok := manifest.Supports(request.PlacementID)
	if !ok {
		outcome.Rejections = append(outcome.Rejections, "placement_not_active")
		e.observeAuction("no_fill")
		return outcome
	}
	endpoints := manifest.ActiveDSPEndpoints()
	if len(endpoints) == 0 {
		outcome.Rejections = append(outcome.Rejections, "no_dsp_endpoints")
		e.observeAuction("no_fill")
		return outcome
	}
	body, err := json.Marshal(buildWireRequest(request))
	if err != nil {
		outcome.Rejections = append(outcome.Rejections, "request_encode_failed")
		e.observeAuction("no_fill")
		return outcome
	}
	deadline := started.Add(request.TMax)
	if request.TMax <= 0 {
		deadline = started.Add(50 * time.Millisecond)
	}
	results := make(chan bidderResult, len(endpoints))
	var wg sync.WaitGroup
	for _, endpoint := range endpoints {
		wg.Add(1)
		go func(endpoint snapshot.DSPEndpoint) {
			defer wg.Done()
			results <- e.callBidder(ctx, endpoint, body, request, deadline)
		}(endpoint)
	}
	wg.Wait()
	close(results)

	var candidates []Bid
	for result := range results {
		if result.err != nil {
			e.observeDSPCall(result.outcome)
			outcome.Rejections = append(outcome.Rejections, "dsp_"+result.outcome)
			continue
		}
		e.observeDSPCall("ok")
		valid, notices, rejected := e.validateResponse(result.endpoint, request, placement, result.response, deadline)
		candidates = append(candidates, valid...)
		outcome.Notices = append(outcome.Notices, notices...)
		outcome.Rejections = append(outcome.Rejections, rejected...)
	}
	if len(candidates) == 0 {
		outcome.Filled = false
		e.observeAuction("no_fill")
		return outcome
	}
	sortCandidates(candidates, request.AuctionID)
	outcome.Winner = candidates[0]
	outcome.Losers = candidates[1:]
	for _, loser := range outcome.Losers {
		if loser.LURL != "" {
			outcome.Notices = append(outcome.Notices, Notice{URL: loser.LURL, Reason: LossHigherBid,
				PriceMicros: loser.CPMMicros, NoticeID: noticeID(request.AuctionID, loser.EndpointID, loser.BidID, LossHigherBid)})
		}
	}
	if outcome.Winner.NURL != "" {
		outcome.Notices = append(outcome.Notices, Notice{URL: outcome.Winner.NURL, Reason: 0,
			PriceMicros: outcome.Winner.CPMMicros, NoticeID: noticeID(request.AuctionID, outcome.Winner.EndpointID, outcome.Winner.BidID, 0)})
	}
	outcome.Filled = true
	e.observeAuction("filled")
	return outcome
}

func (e *Engine) callBidder(ctx context.Context, endpoint snapshot.DSPEndpoint, body []byte, request Request, deadline time.Time) bidderResult {
	budget := time.Until(deadline)
	endpointBudget := time.Duration(endpoint.TimeoutMS) * time.Millisecond
	if endpointBudget <= 0 || endpointBudget > budget {
		endpointBudget = budget
	}
	if endpointBudget < minRenderBudget {
		return bidderResult{endpoint: endpoint, err: context.DeadlineExceeded, outcome: "timeout"}
	}
	callCtx, cancel := context.WithTimeout(ctx, endpointBudget)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint.BidURL, bytes.NewReader(body))
	if err != nil {
		return bidderResult{endpoint: endpoint, err: err, outcome: "error"}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("X-Request-ID", request.AuctionID)
	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return bidderResult{endpoint: endpoint, err: err, outcome: "timeout"}
		}
		return bidderResult{endpoint: endpoint, err: err, outcome: "error"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return bidderResult{endpoint: endpoint, err: fmt.Errorf("dsp status %d", response.StatusCode), outcome: "error"}
	}
	var decoded wireResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBidResponseBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return bidderResult{endpoint: endpoint, err: err, outcome: "error"}
	}
	return bidderResult{endpoint: endpoint, response: decoded, outcome: "ok"}
}

func (e *Engine) validateResponse(endpoint snapshot.DSPEndpoint, request Request, placement snapshot.Placement, response wireResponse, deadline time.Time) ([]Bid, []Notice, []string) {
	if response.ID != request.AuctionID {
		e.observeBidRejected("invalid_response")
		return nil, nil, []string{"bid_response_id_mismatch"}
	}
	currency := strings.ToUpper(strings.TrimSpace(response.Cur))
	if currency == "" {
		currency = placement.Currency
	}
	var valid []Bid
	var notices []Notice
	var rejected []string
	for _, seat := range response.SeatBid {
		for _, wire := range seat.Bid {
			bid := Bid{EndpointID: endpoint.ID, EndpointName: endpoint.Name, Priority: endpoint.Priority,
				BidID: wire.ID, ImpID: wire.ImpID,
				CampaignID: wire.CID, CreativeID: wire.Crid, Currency: currency, Reservation: wire.Ext.ReservationRef,
				BURL: wire.BURL, NURL: wire.NURL, LURL: wire.LURL}
			reject := func(reason string, lossCode int) {
				e.observeBidRejected(reason)
				rejected = append(rejected, reason)
				if wire.LURL != "" && endpoint.CallbackAllowed(wire.LURL) {
					notices = append(notices, Notice{URL: wire.LURL, Reason: lossCode, PriceMicros: bid.CPMMicros,
						NoticeID: noticeID(request.AuctionID, endpoint.ID, wire.ID, lossCode)})
				}
			}
			if wire.ImpID != "1" {
				reject("invalid_response", LossExchangeFilter)
				continue
			}
			if currency != placement.Currency {
				reject("currency_mismatch", LossExchangeFilter)
				continue
			}
			micros, err := ParseCPMMicros(wire.Price.String())
			if err != nil {
				reject("invalid_response", LossExchangeFilter)
				continue
			}
			bid.CPMMicros = micros
			if micros < placement.FloorCPMMicros {
				reject("below_floor", LossBelowFloor)
				continue
			}
			if wire.ID == "" || bid.Reservation == "" {
				reject("invalid_response", LossExchangeFilter)
				continue
			}
			creative, ok := endpoint.ActiveCreative(wire.Crid)
			if !ok || !creativeMatches(wire, creative) {
				reject("creative_not_whitelisted", LossExchangeFilter)
				continue
			}
			if wire.W > 0 && (wire.W != placement.Width || wire.H != placement.Height) {
				reject("creative_not_whitelisted", LossExchangeFilter)
				continue
			}
			if wire.W == 0 && (creative.Width != placement.Width || creative.Height != placement.Height) {
				reject("creative_not_whitelisted", LossExchangeFilter)
				continue
			}
			if !callbackURLsAllowed(endpoint, wire) {
				reject("creative_not_whitelisted", LossExchangeFilter)
				continue
			}
			if time.Until(deadline) < minRenderBudget {
				reject("deadline_expired", LossOpportunityExpired)
				continue
			}
			bid.Creative = creative
			valid = append(valid, bid)
		}
	}
	return valid, notices, rejected
}

func creativeMatches(wire wireBid, creative snapshot.Creative) bool {
	ext := wire.Ext.OpenADS
	if ext == nil {
		return false
	}
	if !strings.EqualFold(ext.AssetRef, creative.AssetRef) || !strings.EqualFold(ext.AssetSHA256, creative.AssetSHA256) {
		return false
	}
	if ext.AssetWidthPX != creative.Width || ext.AssetHeightPX != creative.Height {
		return false
	}
	if !strings.EqualFold(ext.LandingURL, creative.LandingURL) {
		return false
	}
	if _, ok := snapshot.SupportedMIME(creative.MIMEType); !ok {
		return false
	}
	return true
}

func callbackURLsAllowed(endpoint snapshot.DSPEndpoint, bid wireBid) bool {
	for _, raw := range []string{bid.NURL, bid.LURL, bid.BURL} {
		if raw == "" {
			continue
		}
		if !endpoint.CallbackAllowed(raw) {
			return false
		}
	}
	return true
}

func sortCandidates(candidates []Bid, auctionID string) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.CPMMicros != b.CPMMicros {
			return a.CPMMicros > b.CPMMicros
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		hashA, hashB := tieHash(auctionID, a.EndpointID), tieHash(auctionID, b.EndpointID)
		if hashA != hashB {
			return hashA < hashB
		}
		return a.EndpointID < b.EndpointID
	})
}

func tieHash(auctionID, endpointID string) string {
	digest := sha256.Sum256([]byte(auctionID + "|" + endpointID))
	return hex.EncodeToString(digest[:])
}

func noticeID(auctionID, endpointID, bidID string, outcome int) string {
	hash := sha256.New()
	for _, part := range []string{"openads.notice.v1", auctionID, endpointID, bidID, fmt.Sprintf("%d", outcome)} {
		hash.Write([]byte(fmt.Sprintf("%d:", len(part))))
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func buildWireRequest(request Request) wireRequest {
	wire := wireRequest{
		ID:   request.AuctionID,
		AT:   1,
		TMax: int(request.TMax / time.Millisecond),
		Cur:  []string{request.Currency},
		Imp: []wireImp{{
			ID:       "1",
			TagID:    request.PlacementID,
			BidFloor: json.Number(FormatCPMMicros(request.FloorCPMMicros)),
			Currency: request.Currency,
			Banner:   wireBanner{W: request.Width, H: request.Height},
		}},
	}
	if request.SiteDomain != "" || request.SitePage != "" {
		wire.Site = &wireSite{Domain: request.SiteDomain, Page: request.SitePage}
	}
	if len(request.Segments) > 0 {
		segments := make([]wireSegment, 0, len(request.Segments))
		for _, segment := range request.Segments {
			segments = append(segments, wireSegment{ID: segment.ID, Name: segment.Name})
		}
		wire.User = &wireUser{Data: []wireData{{ID: "openads-dmp", Segment: segments}}}
	}
	return wire
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) observeAuction(outcome string) {
	if e.Metrics != nil {
		e.Metrics.ObserveAuction(outcome)
	}
}

func (e *Engine) observeDSPCall(outcome string) {
	if e.Metrics != nil {
		e.Metrics.ObserveDSPCall(outcome)
	}
}

func (e *Engine) observeBidRejected(reason string) {
	if e.Metrics != nil {
		e.Metrics.ObserveBidRejected(reason)
	}
}
