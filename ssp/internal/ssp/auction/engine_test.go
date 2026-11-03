package auction_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/auction"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

const (
	placementID = "018f7a3b2c9d7e1fa3b4c5d6e7f89005"
	creativeID  = "018f7a3b2c9d7e1fa3b4c5d6e7f89008"
	assetSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func creative() snapshot.Creative {
	return snapshot.Creative{ID: creativeID, AssetRef: "as:018f7a3b2c9d7e1fa3b4c5d6e7f89001:" + assetSHA,
		AssetSHA256: assetSHA, Width: 728, Height: 90, MIMEType: "image/png",
		LandingURLHost: "publisher.test", LandingURL: "https://publisher.test/landing",
		Status: snapshot.StatusActive, Version: 1}
}

func manifestFor(endpoints ...snapshot.DSPEndpoint) *snapshot.Manifest {
	return &snapshot.Manifest{
		Version: snapshot.FormatVersion(1),
		Publishers: map[string]snapshot.Publisher{
			"018f7a3b2c9d7e1fa3b4c5d6e7f89001": {ID: "018f7a3b2c9d7e1fa3b4c5d6e7f89001", Status: snapshot.StatusActive},
		},
		Properties: map[string]snapshot.Property{
			"018f7a3b2c9d7e1fa3b4c5d6e7f89002": {ID: "018f7a3b2c9d7e1fa3b4c5d6e7f89002", PublisherID: "018f7a3b2c9d7e1fa3b4c5d6e7f89001", Status: snapshot.StatusActive},
		},
		AuctionPolicies: map[string]snapshot.AuctionPolicy{
			"018f7a3b2c9d7e1fa3b4c5d6e7f89003": {ID: "018f7a3b2c9d7e1fa3b4c5d6e7f89003", At: 1, TieBreaker: snapshot.TieBreakerPriorityThenHash,
				FloorEnforcement: snapshot.FloorEnforcementStrict, CurrencyPolicy: snapshot.CurrencyPolicyExactMatch, Status: snapshot.StatusActive},
		},
		Placements: map[string]snapshot.Placement{
			placementID: {ID: placementID, PropertyID: "018f7a3b2c9d7e1fa3b4c5d6e7f89002", AuctionPolicyID: "018f7a3b2c9d7e1fa3b4c5d6e7f89003",
				Width: 728, Height: 90, FloorCPMMicros: 1_000_000, Currency: "USD", Status: snapshot.StatusActive},
		},
		DSPEndpoints: func() map[string]snapshot.DSPEndpoint {
			result := make(map[string]snapshot.DSPEndpoint)
			for _, endpoint := range endpoints {
				result[endpoint.ID] = endpoint
			}
			return result
		}(),
	}
}

func engine() *auction.Engine {
	return auction.NewEngine(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), auction.AllowPrivateTargets())
}

func bidder(t *testing.T, price float64, mutate func(map[string]any)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		creative := creative()
		bid := map[string]any{
			"id": "bid-1", "impid": "1", "price": price, "crid": creativeID, "cid": "campaign-1",
			"w": 728, "h": 90, "nurl": "http://127.0.0.1/win", "lurl": "http://127.0.0.1/loss", "burl": "http://127.0.0.1/bill",
			"ext": map[string]any{
				"reservation_ref": "reservation-token",
				"openads": map[string]any{
					"asset_ref": creative.AssetRef, "asset_sha256": creative.AssetSHA256,
					"asset_width_px": 728, "asset_height_px": 90, "landing_url": creative.LandingURL,
				},
			},
		}
		response := map[string]any{
			"id": "auction-1", "cur": "USD",
			"seatbid": []any{map[string]any{"bid": []any{bid}}},
		}
		if mutate != nil {
			mutate(response)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func endpoint(id string, priority int, url string) snapshot.DSPEndpoint {
	return snapshot.DSPEndpoint{ID: id, Name: id, BidURL: url, OpenRTBProfile: "openrtb2.6",
		CallbackHosts: []string{"127.0.0.1"}, Priority: priority, TimeoutMS: 200, Status: snapshot.StatusActive,
		CreativeWhitelist: map[string]snapshot.Creative{creativeID: creative()}}
}

func request() auction.Request {
	return auction.Request{AuctionID: "auction-1", PlacementID: placementID, Width: 728, Height: 90,
		Currency: "USD", FloorCPMMicros: 1_000_000, TMax: 150 * time.Millisecond}
}

func TestHighestPriceWinsAndLoserIsNotified(t *testing.T) {
	low, _ := bidder(t, 1.5, nil)
	high, _ := bidder(t, 2.5, nil)
	manifest := manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 10, low.URL),
		endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89011", 20, high.URL))
	outcome := engine().Run(context.Background(), manifest, request())
	if !outcome.Filled || outcome.Winner.CPMMicros != 2_500_000 {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	losses := 0
	for _, notice := range outcome.Notices {
		if notice.Reason == auction.LossHigherBid {
			losses++
		}
	}
	if losses != 1 {
		t.Fatalf("expected one losing bidder notice, got %v", outcome.Notices)
	}
}

func TestPriorityAndHashBreakPriceTies(t *testing.T) {
	first, _ := bidder(t, 2.0, nil)
	second, _ := bidder(t, 2.0, nil)
	manifest := manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 20, first.URL),
		endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89011", 5, second.URL))
	outcome := engine().Run(context.Background(), manifest, request())
	if !outcome.Filled || outcome.Winner.Priority != 5 {
		t.Fatalf("expected lower priority number to win the tie: %+v", outcome.Winner)
	}
}

func TestSlowBidderDoesNotDelayWinner(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		_, _ = w.Write([]byte(`{"id":"auction-1","nbr":2}`))
	}))
	defer slow.Close()
	fast, _ := bidder(t, 2.0, nil)
	slowEndpoint := endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 10, slow.URL)
	slowEndpoint.TimeoutMS = 10
	manifest := manifestFor(slowEndpoint, endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89011", 20, fast.URL))
	started := time.Now()
	outcome := engine().Run(context.Background(), manifest, request())
	if !outcome.Filled || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("slow bidder blocked the auction: %+v in %v", outcome, time.Since(started))
	}
}

func TestBelowFloorAndUnsafeBidsAreRejected(t *testing.T) {
	below, _ := bidder(t, 0.5, nil)
	outcome := engine().Run(context.Background(), manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 10, below.URL)), request())
	if outcome.Filled || !contains(outcome.Rejections, "below_floor") {
		t.Fatalf("expected below floor rejection: %+v", outcome)
	}
	unsafe, _ := bidder(t, 2.0, func(response map[string]any) {
		seat := response["seatbid"].([]any)[0].(map[string]any)
		bid := seat["bid"].([]any)[0].(map[string]any)
		bid["lurl"] = "http://evil.example/loss"
	})
	outcome = engine().Run(context.Background(), manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89011", 10, unsafe.URL)), request())
	if outcome.Filled {
		t.Fatal("bid with a non-allowlisted callback host must be rejected")
	}
}

func TestCreativeMetadataMismatchIsRejected(t *testing.T) {
	mismatch, _ := bidder(t, 2.0, func(response map[string]any) {
		seat := response["seatbid"].([]any)[0].(map[string]any)
		bid := seat["bid"].([]any)[0].(map[string]any)
		bid["crid"] = "018f7a3b2c9d7e1fa3b4c5d6e7f89099"
	})
	outcome := engine().Run(context.Background(), manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 10, mismatch.URL)), request())
	if outcome.Filled || !contains(outcome.Rejections, "creative_not_whitelisted") {
		t.Fatalf("expected creative whitelist rejection: %+v", outcome)
	}
}

func TestCurrencyMismatchAndWrongResponseIDAreRejected(t *testing.T) {
	wrongCurrency, _ := bidder(t, 2.0, func(response map[string]any) {
		response["cur"] = "EUR"
	})
	outcome := engine().Run(context.Background(), manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 10, wrongCurrency.URL)), request())
	if outcome.Filled || !contains(outcome.Rejections, "currency_mismatch") {
		t.Fatalf("expected currency rejection: %+v", outcome)
	}
	wrongID, _ := bidder(t, 2.0, func(response map[string]any) {
		response["id"] = "other-auction"
	})
	outcome = engine().Run(context.Background(), manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89011", 10, wrongID.URL)), request())
	if outcome.Filled {
		t.Fatal("mismatched response id must not win")
	}
}

func TestNotifyDeliversLossNoticeWithMacros(t *testing.T) {
	var delivered atomic.Int32
	noticeTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
		if r.Header.Get("X-Notice-ID") == "" {
			t.Error("missing notice id")
		}
		if strings.Contains(r.URL.RawQuery, "${") {
			t.Error("notice macros were not expanded")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer noticeTarget.Close()
	low, _ := bidder(t, 1.5, func(response map[string]any) {
		seat := response["seatbid"].([]any)[0].(map[string]any)
		bid := seat["bid"].([]any)[0].(map[string]any)
		bid["lurl"] = noticeTarget.URL + "/loss?reason=${AUCTION_LOSS}&price=${AUCTION_PRICE}"
		bid["nurl"] = noticeTarget.URL + "/win?price=${AUCTION_PRICE}"
	})
	high, _ := bidder(t, 2.5, func(response map[string]any) {
		seat := response["seatbid"].([]any)[0].(map[string]any)
		bid := seat["bid"].([]any)[0].(map[string]any)
		bid["nurl"] = noticeTarget.URL + "/win?price=${AUCTION_PRICE}"
	})
	manifest := manifestFor(endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89010", 10, low.URL),
		endpoint("018f7a3b2c9d7e1fa3b4c5d6e7f89011", 20, high.URL))
	outcome := engine().Run(context.Background(), manifest, request())
	engine().Notify(context.Background(), outcome)
	deadline := time.Now().Add(time.Second)
	for delivered.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if delivered.Load() != 2 {
		t.Fatalf("expected win and loss notices, got %d", delivered.Load())
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
