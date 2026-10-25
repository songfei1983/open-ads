package decision

import (
	"testing"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
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
