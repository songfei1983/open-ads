package http_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapterHTTP "github.com/songfei1983/open-ads/ssp/internal/ssp/adapter/http"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/config"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/dmp"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/render"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/token"
)

const (
	testTenant    = "018f7a3b2c9d7e1fa3b4c5d6e7f89001"
	testPublisher = "018f7a3b2c9d7e1fa3b4c5d6e7f89002"
	testProperty  = "018f7a3b2c9d7e1fa3b4c5d6e7f89003"
	testPolicy    = "018f7a3b2c9d7e1fa3b4c5d6e7f89004"
	testPlacement = "018f7a3b2c9d7e1fa3b4c5d6e7f89005"
	testSupply    = "018f7a3b2c9d7e1fa3b4c5d6e7f89006"
	testDSP       = "018f7a3b2c9d7e1fa3b4c5d6e7f89007"
	testCreative  = "018f7a3b2c9d7e1fa3b4c5d6e7f89008"
	supplySecret  = "supply-secret-value-0123456789"
)

type harness struct {
	server   *adapterHTTP.Server
	ts       *httptest.Server
	dsp      *httptest.Server
	dmpCalls *atomic.Int32
	asset    []byte
	digest   string
}

func newHarness(t *testing.T, dmpStatus int) *harness {
	t.Helper()
	asset := []byte("png-bytes-for-render")
	digest := sha256Hex(asset)
	root := t.TempDir()
	dir := filepath.Join(root, testTenant, "assets", digest[0:2], digest[2:4])
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, digest), asset, 0o640); err != nil {
		t.Fatal(err)
	}

	var dmpCalls atomic.Int32
	dmpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dmpCalls.Add(1)
		if dmpStatus != http.StatusOK {
			w.WriteHeader(dmpStatus)
			return
		}
		_, _ = w.Write([]byte(`{"segments":[{"id":"seg-golf","name":"Golf"}]}`))
	}))
	t.Cleanup(dmpServer.Close)

	creative := snapshot.Creative{ID: testCreative, AssetRef: "as:" + testTenant + ":" + digest,
		AssetSHA256: digest, Width: 728, Height: 90, MIMEType: "image/png",
		LandingURLHost: "publisher.test", LandingURL: "https://publisher.test/landing",
		Status: snapshot.StatusActive, Version: 1}

	dsp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/openrtb2/bid":
			var request struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			response := map[string]any{
				"id": request.ID, "cur": "USD",
				"seatbid": []any{map[string]any{"bid": []any{map[string]any{
					"id": "bid-1", "impid": "1", "price": 2.5, "crid": testCreative, "cid": "campaign-1", "w": 728, "h": 90,
					"nurl": "http://127.0.0.1/win", "lurl": "http://127.0.0.1/loss", "burl": "http://127.0.0.1/bill",
					"ext": map[string]any{"reservation_ref": "reservation-token", "openads": map[string]any{
						"asset_ref": creative.AssetRef, "asset_sha256": creative.AssetSHA256,
						"asset_width_px": 728, "asset_height_px": 90, "landing_url": creative.LandingURL,
					}},
				}}}},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(dsp.Close)

	manifest := &snapshot.Manifest{
		Version: snapshot.FormatVersion(3),
		Publishers: map[string]snapshot.Publisher{
			testPublisher: {ID: testPublisher, Name: "Acme", Status: snapshot.StatusActive, Version: 1},
		},
		Properties: map[string]snapshot.Property{
			testProperty: {ID: testProperty, PublisherID: testPublisher, Name: "Site", Type: "site", ExternalID: "publisher.test", Status: snapshot.StatusActive, Version: 1},
		},
		AuctionPolicies: map[string]snapshot.AuctionPolicy{
			testPolicy: {ID: testPolicy, Name: "First price", At: 1, TieBreaker: snapshot.TieBreakerPriorityThenHash,
				FloorEnforcement: snapshot.FloorEnforcementStrict, CurrencyPolicy: snapshot.CurrencyPolicyExactMatch, Status: snapshot.StatusActive, Version: 1},
		},
		Placements: map[string]snapshot.Placement{
			testPlacement: {ID: testPlacement, PropertyID: testProperty, AuctionPolicyID: testPolicy, Name: "Leaderboard",
				MediaType: "banner", Width: 728, Height: 90, FloorCPMMicros: 1_000_000, Currency: "USD",
				PrivacyPolicy: "consent_required", AllowedOrigins: []string{"https://publisher.test"}, Status: snapshot.StatusActive, Version: 1},
		},
		SupplyEndpoints: map[string]snapshot.SupplyEndpoint{
			testSupply: {ID: testSupply, PropertyID: testProperty, Name: "Supply", ProtocolVersion: "openrtb2.6",
				CredentialSHA256: snapshot.HashSHA256Hex(supplySecret), AllowedOrigins: []string{"https://publisher.test"},
				RateLimitPerSecond: 100, Status: snapshot.StatusActive, Version: 1},
		},
		DSPEndpoints: map[string]snapshot.DSPEndpoint{
			testDSP: {ID: testDSP, Name: "DSP", BidURL: dsp.URL + "/openrtb2/bid", OpenRTBProfile: "openrtb2.6",
				CallbackHosts: []string{"127.0.0.1"}, Priority: 1, TimeoutMS: 100, Status: snapshot.StatusActive, Version: 1,
				CreativeWhitelist: map[string]snapshot.Creative{testCreative: creative}},
		},
	}
	if err := snapshot.Activate(manifest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.Reset)

	cfg := &config.Config{
		ServiceName: "test-ssp", Port: 8080, AdminTenantID: testTenant,
		AuctionTimeout: 150 * time.Millisecond, DMPTimeout: 50 * time.Millisecond, DSPTimeout: 50 * time.Millisecond,
		RenderTokenTTL: time.Minute, EventTokenTTL: time.Hour, MaxSegments: 10, DMPNamespace: "publisher-account-v1",
		AllowPrivateCallbacks: true,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := adapterHTTP.NewServer(cfg, logger)
	codec, err := token.NewCodec([]byte(strings.Repeat("k", 32)), 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetTokenCodec(codec)
	store, err := render.NewReadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetRenderer(&render.Renderer{Codec: codec, Store: store, TenantID: testTenant})
	srv.SetDMPAdapter(&dmp.Adapter{BaseURL: dmpServer.URL, TenantID: testTenant, Namespace: "publisher-account-v1",
		HMACKey: []byte(strings.Repeat("h", 32)), Timeout: 50 * time.Millisecond, Client: &http.Client{Timeout: time.Second}})
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{server: srv, ts: ts, dsp: dsp, dmpCalls: &dmpCalls, asset: asset, digest: digest}
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func TestSDKFilledRendersSSPHostedCreative(t *testing.T) {
	h := newHarness(t, http.StatusOK)
	body := `{"request_id":"018f7a3b2c9d7e1fa3b4c5d6e7f89009","placement_id":"` + testPlacement +
		`","slot":{"id":"leaderboard","width":728,"height":90},"page":{"origin":"https://publisher.test","path":"/demo"},"consent":{"source":"test-fixture","value":"publisher-pseudonym"}}`
	request, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/sdk/v1/ad-requests", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://publisher.test")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	if response.Header.Get("Access-Control-Allow-Origin") != "https://publisher.test" {
		t.Fatalf("missing exact CORS origin: %v", response.Header)
	}
	var ad struct {
		Status string `json:"status"`
		Ad     struct {
			CreativeID string `json:"creative_id"`
			RenderURL  string `json:"render_url"`
			ClickURL   string `json:"click_url"`
			EventToken string `json:"event_token"`
		} `json:"ad"`
		Diagnostics map[string]any `json:"diagnostics"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ad); err != nil {
		t.Fatal(err)
	}
	if ad.Status != "filled" || ad.Ad.CreativeID != testCreative {
		t.Fatalf("unexpected ad response: %+v", ad)
	}
	if !strings.HasPrefix(ad.Ad.RenderURL, h.ts.URL+"/render/v1/") || !strings.HasPrefix(ad.Ad.ClickURL, h.ts.URL+"/click/v1/") {
		t.Fatalf("render/click URLs must be SSP origin: %s %s", ad.Ad.RenderURL, ad.Ad.ClickURL)
	}
	if h.dmpCalls.Load() != 1 {
		t.Fatalf("expected one DMP lookup, got %d", h.dmpCalls.Load())
	}
	shellResponse, err := http.Get(ad.Ad.RenderURL)
	if err != nil {
		t.Fatal(err)
	}
	defer shellResponse.Body.Close()
	shellBytes, _ := io.ReadAll(shellResponse.Body)
	shell := string(shellBytes)
	if shellResponse.StatusCode != http.StatusOK || !strings.Contains(shellResponse.Header.Get("Content-Security-Policy"), "frame-ancestors https://publisher.test") {
		t.Fatalf("unexpected shell: %d %v", shellResponse.StatusCode, shellResponse.Header)
	}
	const marker = "/render/v1/assets/"
	index := strings.Index(shell, marker)
	if index < 0 {
		t.Fatalf("shell has no asset URL: %s", shell)
	}
	assetPath := shell[index:]
	if end := strings.IndexByte(assetPath, '"'); end >= 0 {
		assetPath = assetPath[:end]
	}
	assetResponse, err := http.Get(h.ts.URL + assetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer assetResponse.Body.Close()
	assetBytes, _ := io.ReadAll(assetResponse.Body)
	if assetResponse.StatusCode != http.StatusOK || string(assetBytes) != string(h.asset) {
		t.Fatalf("unexpected asset: %d %q", assetResponse.StatusCode, assetBytes)
	}
	if assetResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("render bytes must not be cached")
	}
}

func TestSDKFailsClosedWhenDMPUnavailable(t *testing.T) {
	h := newHarness(t, http.StatusServiceUnavailable)
	body := `{"placement_id":"` + testPlacement + `","slot":{"width":728,"height":90},"page":{"origin":"https://publisher.test","path":"/demo"},"consent":{"value":"publisher-pseudonym"}}`
	request, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/sdk/v1/ad-requests", strings.NewReader(body))
	request.Header.Set("Origin", "https://publisher.test")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Status string `json:"status"`
	}
	_ = json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode != http.StatusOK || result.Status != "no_fill" {
		t.Fatalf("expected fail-closed no_fill, got %d %+v", response.StatusCode, result)
	}
}

func TestSDKRejectsUnknownOriginBeforeAuction(t *testing.T) {
	h := newHarness(t, http.StatusOK)
	body := `{"placement_id":"` + testPlacement + `","slot":{"width":728,"height":90},"page":{"origin":"https://publisher.test","path":"/demo"}}`
	request, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/sdk/v1/ad-requests", strings.NewReader(body))
	request.Header.Set("Origin", "https://evil.test")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.StatusCode)
	}
	if h.dmpCalls.Load() != 0 {
		t.Fatal("origin rejection must not trigger downstream calls")
	}
}

func TestSupplyIngressAuthenticatesAndAuctions(t *testing.T) {
	h := newHarness(t, http.StatusOK)
	body := `{"id":"rtb-request-1","imp":[{"id":"1","tagid":"` + testPlacement + `","banner":{"w":728,"h":90}}],"site":{"domain":"publisher.test","page":"https://publisher.test/demo"},"tmax":120,"cur":["USD"]}`
	request, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/openrtb2/auction", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Supply-Key", supplySecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var bidResponse struct {
		ID      string `json:"id"`
		SeatBid []struct {
			Bid []struct {
				Price float64 `json:"price"`
				CRID  string  `json:"crid"`
				ADM   string  `json:"adm"`
			} `json:"bid"`
		} `json:"seatbid"`
	}
	if err := json.NewDecoder(response.Body).Decode(&bidResponse); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || bidResponse.ID != "rtb-request-1" || len(bidResponse.SeatBid) != 1 {
		t.Fatalf("unexpected supply response: %d %+v", response.StatusCode, bidResponse)
	}
	bid := bidResponse.SeatBid[0].Bid[0]
	if bid.Price != 2.5 || bid.CRID != testCreative || !strings.Contains(bid.ADM, "/render/v1/") {
		t.Fatalf("unexpected bid: %+v", bid)
	}

	unauthorized, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/openrtb2/auction", strings.NewReader(body))
	unauthorized.Header.Set("X-Supply-Key", "wrong-secret-value-0123456789")
	unauthorizedResponse, err := http.DefaultClient.Do(unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorizedResponse.Body.Close()
	if unauthorizedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorizedResponse.StatusCode)
	}

	mismatchBody := `{"id":"rtb-2","imp":[{"id":"1","tagid":"` + testPlacement + `","banner":{"w":728,"h":90}}],"site":{"domain":"evil.test"}}`
	mismatch, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/openrtb2/auction", strings.NewReader(mismatchBody))
	mismatch.Header.Set("X-Supply-Key", supplySecret)
	mismatchResponse, err := http.DefaultClient.Do(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	defer mismatchResponse.Body.Close()
	if mismatchResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("expected supply mismatch 403, got %d", mismatchResponse.StatusCode)
	}
}

func TestProbesAndMissingConfiguration(t *testing.T) {
	snapshot.Reset()
	cfg := &config.Config{ServiceName: "test-ssp", Port: 8080, AuctionTimeout: 80 * time.Millisecond,
		RenderTokenTTL: time.Minute, EventTokenTTL: time.Hour, MaxSegments: 10, AdminTenantID: testTenant,
		AllowPrivateCallbacks: true}
	srv := adapterHTTP.NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	health, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("expected healthz 200, got %d", health.StatusCode)
	}
	ready, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Body.Close()
	if ready.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected not-ready without a manifest, got %d", ready.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/sdk/v1/ad-requests", strings.NewReader(`{"placement_id":"x","slot":{"width":1,"height":1}}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without configuration, got %d", response.StatusCode)
	}
}
