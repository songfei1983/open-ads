package decision

import (
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/lease"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

func TestNoBidDecisioner(t *testing.T) {
	response, err := (NoBidDecisioner{}).Decide(t.Context(), Request{ID: "request-1", ImpressionCount: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response.RequestID != "request-1" || !response.NoBid || response.NoBidReason != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestFixtureDecisionerTargeting(t *testing.T) {
	const version = "fixture-targeting-test-v1"
	manifest := &snapshot.Manifest{
		Version: version,
		Campaigns: map[string]domain.Campaign{
			"campaign-1": {ID: "campaign-1", Active: true, LineItemIDs: []string{"line-1"}},
		},
		LineItems: map[string]domain.LineItem{
			"line-1": {
				ID: "line-1", CampaignID: "campaign-1", Active: true,
				CreativeIDs: []string{"creative-1"}, SiteDomain: "example.com",
				SitePage: "https://example.com/news", BannerWidth: 300, BannerHeight: 250,
			},
		},
		Creatives: map[string]domain.Creative{
			"creative-1": {
				ID: "creative-1", Active: true,
				AssetMeta: domain.AssetMeta{MIMEType: "image/png", SizeKB: 20, Hash: "sha256:abc", Host: "assets.example.com"},
			},
		},
	}
	if err := snapshot.Register(manifest); err != nil {
		t.Fatalf("register manifest: %v", err)
	}

	decisioner := FixtureDecisioner{
		ManifestVersion: version,
		Whitelist:       []domain.WhiteListEntry{{Host: "assets.example.com", MIME: "image/png"}},
	}
	base := Request{
		ID: "request-1", ImpressionCount: 1, SiteDomain: "EXAMPLE.COM.",
		SitePage: "https://example.com/news", BannerWidth: 300, BannerHeight: 250,
	}
	tests := []struct {
		name      string
		request   Request
		candidate bool
	}{
		{name: "exact targeting match", request: base, candidate: true},
		{name: "domain mismatch", request: func() Request { r := base; r.SiteDomain = "other.example"; return r }()},
		{name: "page mismatch", request: func() Request { r := base; r.SitePage += "/"; return r }()},
		{name: "banner mismatch", request: func() Request { r := base; r.BannerWidth = 320; return r }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decisioner.Decide(t.Context(), tt.request)
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if got.Candidate != tt.candidate {
				t.Fatalf("Candidate = %v, want %v", got.Candidate, tt.candidate)
			}
			if !got.NoBid {
				t.Fatal("candidate evaluation must remain no-bid before budget reservation")
			}
		})
	}
}

func TestFixtureDecisionerWithBudgetReservation(t *testing.T) {
	const version = "fixture-budget-test-v1"
	manifest := &snapshot.Manifest{
		Version: version,
		Campaigns: map[string]domain.Campaign{
			"campaign-1": {ID: "campaign-1", Active: true, LineItemIDs: []string{"line-1"}},
		},
		LineItems: map[string]domain.LineItem{
			"line-1": {
				ID: "line-1", CampaignID: "campaign-1", Active: true,
				CreativeIDs: []string{"creative-1"}, SiteDomain: "example.com",
				SitePage: "https://example.com/news", BannerWidth: 300, BannerHeight: 250,
				BidCPMMicros: 2_500_000, Currency: "USD",
			},
		},
		Creatives: map[string]domain.Creative{
			"creative-1": {
				ID: "creative-1", Active: true,
				AssetMeta: domain.AssetMeta{MIMEType: "image/png", SizeKB: 20, Hash: "sha256:abc", Host: "assets.example.com"},
			},
		},
	}
	if err := snapshot.Register(manifest); err != nil {
		t.Fatalf("register manifest: %v", err)
	}

	allocator := lease.NewBudgetAllocator()
	now := time.Now().UTC()
	// Install budget for daily and lifetime scopes
	if err := allocator.InstallLease("line-1:daily", "lease-daily-1", 5000, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := allocator.InstallLease("line-1:lifetime", "lease-life-1", 5000, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	codec, err := lease.NewTokenCodec("v1", key)
	if err != nil {
		t.Fatal(err)
	}

	decisioner := FixtureDecisioner{
		ManifestVersion: version,
		Whitelist:       []domain.WhiteListEntry{{Host: "assets.example.com", MIME: "image/png"}},
		Reserver:        allocator,
		TokenCodec:      codec,
		BaseNoticeURL:   "https://dsp.test.example",
		TenantID:        "018f7a3b2c9d7e1fa3b4c5d6e7f89001",
	}

	req := Request{
		ID: "auction-req-1", ImpressionCount: 1, SiteDomain: "example.com",
		SitePage: "https://example.com/news", BannerWidth: 300, BannerHeight: 250,
		Currencies: []string{"USD"},
	}

	// 1. With sufficient budget -> valid bid response
	resp, err := decisioner.Decide(t.Context(), req)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if resp.NoBid {
		t.Fatalf("expected valid bid, got no-bid reason %d", resp.NoBidReason)
	}
	if resp.Price != 2.50 {
		t.Fatalf("expected price 2.50, got %f", resp.Price)
	}
	if resp.BidID == "" || resp.ReservationID == "" || resp.ReservationToken == "" {
		t.Fatalf("missing bid/reservation fields: %+v", resp)
	}
	if resp.WinNoticeURL == "" || resp.BillableNoticeURL == "" || resp.LossNoticeURL == "" {
		t.Fatalf("missing notice URLs: %+v", resp)
	}

	// Verify the sealed token can be opened
	claims, err := codec.Open(resp.ReservationToken, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("open reservation token error = %v", err)
	}
	if claims.ReservationID != resp.ReservationID || claims.BidID != resp.BidID || claims.ReservedMicros != 2500 {
		t.Fatalf("claims mismatch: %+v", claims)
	}

	// 2. Deplete budget until insufficient -> fail-closed no-bid
	// Available was 5000 - 2500 = 2500. One more reservation will leave 0.
	_, err = decisioner.Decide(t.Context(), req)
	if err != nil {
		t.Fatalf("second Decide() error = %v", err)
	}

	// Third reservation must fail due to insufficient budget
	resp3, err := decisioner.Decide(t.Context(), req)
	if err != nil {
		t.Fatalf("third Decide() error = %v", err)
	}
	if !resp3.NoBid || resp3.NoBidReason != 2 {
		t.Fatalf("expected no-bid when budget exhausted, got %+v", resp3)
	}
	if !resp3.Candidate {
		t.Fatal("expected candidate to still be recognized even when budget unavailable")
	}
}
