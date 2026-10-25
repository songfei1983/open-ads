package decision

import (
	"context"
	"strings"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

// FixtureDecisioner evaluates the published in-process manifest. It identifies
// eligible candidates but still returns no-bid until a budget reservation is
// integrated into the decision path.
type FixtureDecisioner struct {
	ManifestVersion string
	Whitelist       []domain.WhiteListEntry
}

func (f FixtureDecisioner) Decide(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	man, err := snapshot.Load(f.ManifestVersion)
	if err != nil {
		return noBid(req.ID), nil
	}
	if req.ImpressionCount != 1 || req.BannerWidth <= 0 || req.BannerHeight <= 0 {
		return noBid(req.ID), nil
	}

	domainName := normalizeDomain(req.SiteDomain)
	for _, campaign := range man.Campaigns {
		if !campaign.Active {
			continue
		}
		for _, lineItemID := range campaign.LineItemIDs {
			lineItem, ok := man.LineItems[lineItemID]
			if !ok || !lineItem.Active || lineItem.CampaignID != campaign.ID {
				continue
			}
			if !matchesTargeting(lineItem, domainName, req.SitePage, req.BannerWidth, req.BannerHeight) {
				continue
			}
			if hasValidCreative(man, lineItem, f.Whitelist) {
				// Candidate detection is safe, but a valid bid requires an atomic
				// reservation. Keep the response no-bid until that is available.
				return Response{RequestID: req.ID, NoBid: true, NoBidReason: 2, Candidate: true}, nil
			}
		}
	}
	return noBid(req.ID), nil
}

func matchesTargeting(lineItem domain.LineItem, siteDomain, sitePage string, width, height int) bool {
	if lineItem.SiteDomain != "" && normalizeDomain(lineItem.SiteDomain) != siteDomain {
		return false
	}
	if lineItem.SitePage != "" && lineItem.SitePage != sitePage {
		return false
	}
	if lineItem.BannerWidth != width || lineItem.BannerHeight != height {
		return false
	}
	return true
}

func hasValidCreative(manifest *snapshot.Manifest, lineItem domain.LineItem, whitelist []domain.WhiteListEntry) bool {
	for _, creativeID := range lineItem.CreativeIDs {
		creative, ok := manifest.Creatives[creativeID]
		if !ok || !creative.Active {
			continue
		}
		asset := creative.AssetMeta
		if asset.SizeKB <= 0 || asset.Hash == "" || !supportedImageMIME(asset.MIMEType) {
			continue
		}
		for _, allowed := range whitelist {
			if allowed.Host == asset.Host && allowed.MIME == asset.MIMEType {
				return true
			}
		}
	}
	return false
}

func supportedImageMIME(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func normalizeDomain(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func noBid(requestID string) Response {
	return Response{RequestID: requestID, NoBid: true, NoBidReason: 2}
}

// NewFixtureDecisioner creates a fixture policy for a single known asset host.
func NewFixtureDecisioner(version string) FixtureDecisioner {
	return FixtureDecisioner{
		ManifestVersion: version,
		Whitelist:       []domain.WhiteListEntry{{Host: "example.com", MIME: "image/png"}},
	}
}
