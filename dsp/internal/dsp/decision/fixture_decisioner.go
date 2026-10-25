package decision

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/lease"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

// FixtureDecisioner evaluates the published in-process manifest. It identifies
// eligible candidates and, when configured with a budget Reserver, atomically
// acquires a budget reservation and returns a valid bid response.
type FixtureDecisioner struct {
	ManifestVersion string
	Whitelist       []domain.WhiteListEntry
	Reserver        lease.Reserver
	TokenCodec      *lease.TokenCodec
	BaseNoticeURL   string
	TenantID        string
}

func (f FixtureDecisioner) Decide(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	var man *snapshot.Manifest
	var err error
	if f.ManifestVersion == "" {
		man, _, err = snapshot.LoadCurrent()
	} else {
		man, err = snapshot.Load(f.ManifestVersion)
	}
	if err != nil {
		return noBid(req.ID), nil
	}
	if req.ImpressionCount != 1 || req.BannerWidth <= 0 || req.BannerHeight <= 0 {
		return noBid(req.ID), nil
	}

	domainName := normalizeDomain(req.SiteDomain)
	now := time.Now().UTC()
	lineItemIDs := make([]string, 0, len(man.LineItems))
	for id := range man.LineItems {
		lineItemIDs = append(lineItemIDs, id)
	}
	sort.Strings(lineItemIDs)
	var best domain.LineItem
	for _, lineItemID := range lineItemIDs {
		lineItem := man.LineItems[lineItemID]
		campaign, ok := man.Campaigns[lineItem.CampaignID]
		if !ok || !campaign.Active || !withinSchedule(now, campaign.StartsAt, campaign.EndsAt) {
			continue
		}
		lineItemStartsAt := time.Time{}
		if lineItem.StartsAt != nil {
			lineItemStartsAt = *lineItem.StartsAt
		}
		if !lineItem.Active || !withinSchedule(now, lineItemStartsAt, lineItem.EndsAt) || !currencyMatches(lineItem.Currency, req.Currencies) {
			continue
		}
		if !matchesTargeting(lineItem, domainName, req.SitePage, req.BannerWidth, req.BannerHeight) || !hasValidCreative(man, lineItem, f.Whitelist) {
			continue
		}
		if best.ID == "" || lineItem.BidCPMMicros > best.BidCPMMicros || lineItem.BidCPMMicros == best.BidCPMMicros && lineItem.ID < best.ID {
			best = lineItem
		}
	}
	if best.ID != "" {
		if f.Reserver == nil {
			// A valid bid remains forbidden until the budget reservation succeeds.
			return Response{RequestID: req.ID, NoBid: true, NoBidReason: 2, Candidate: true,
				CandidateLineItemID: best.ID, CandidateBidCPMMicros: best.BidCPMMicros}, nil
		}

		spendRisk, _ := domain.CalculateSpendFromCPM(domain.Micros(best.BidCPMMicros), 1)
		if spendRisk <= 0 {
			spendRisk = 1
		}

		var demands []lease.Demand
		dailyScope := best.ID + ":daily"
		if f.Reserver.Available(dailyScope, now) > 0 {
			demands = append(demands, lease.Demand{ScopeID: dailyScope, AmountMicros: spendRisk})
		} else if f.Reserver.Available("daily", now) > 0 {
			demands = append(demands, lease.Demand{ScopeID: "daily", AmountMicros: spendRisk})
		}
		lifetimeScope := best.ID + ":lifetime"
		if f.Reserver.Available(lifetimeScope, now) > 0 {
			demands = append(demands, lease.Demand{ScopeID: lifetimeScope, AmountMicros: spendRisk})
		} else if f.Reserver.Available("lifetime", now) > 0 {
			demands = append(demands, lease.Demand{ScopeID: "lifetime", AmountMicros: spendRisk})
		}
		if len(demands) == 0 {
			if f.Reserver.Available(best.ID, now) > 0 {
				demands = append(demands, lease.Demand{ScopeID: best.ID, AmountMicros: spendRisk})
			} else {
				demands = append(demands, lease.Demand{ScopeID: "daily", AmountMicros: spendRisk})
			}
		}

		resUUID, _ := domain.NewUUIDv7()
		resID := "res-" + resUUID
		res, err := f.Reserver.Reserve(resID, demands, now)
		if err != nil {
			// Safe fail-closed no-bid when budget is insufficient or unavailable
			return Response{
				RequestID:             req.ID,
				NoBid:                 true,
				NoBidReason:           2,
				Candidate:             true,
				CandidateLineItemID:   best.ID,
				CandidateBidCPMMicros: best.BidCPMMicros,
			}, nil
		}

		bidUUID, _ := domain.NewUUIDv7()
		bidID := "bid-" + bidUUID
		impID := "1"
		price := float64(best.BidCPMMicros) / 1_000_000.0
		crid := ""
		if len(best.CreativeIDs) > 0 {
			crid = best.CreativeIDs[0]
		}
		tenantID := f.TenantID
		if tenantID == "" {
			tenantID = "00000000000000000000000000000001"
		}
		var token string
		if f.TokenCodec != nil {
			claims := lease.ReservationClaims{
				TenantID:       tenantID,
				ReservationID:  res.ID,
				AuctionID:      req.ID,
				BidID:          bidID,
				ImpressionID:   impID,
				Currency:       best.Currency,
				ReservedMicros: int64(spendRisk),
				ExpiresAt:      now.Add(10 * time.Minute),
				Demands:        res.Demands,
				LeaseIDs:       res.LeaseIDs,
			}
			token, _ = f.TokenCodec.Seal(claims)
		}
		baseURL := f.BaseNoticeURL
		if baseURL == "" {
			baseURL = "http://127.0.0.1:8081"
		}
		nurl := baseURL + "/rtb/v1/win/" + bidID + "?price=${AUCTION_PRICE}"
		burl := baseURL + "/rtb/v1/billable/" + bidID
		lurl := baseURL + "/rtb/v1/loss/" + bidID + "?reason=${AUCTION_LOSS}"
		if token != "" {
			lurl += "&ref=" + token
		}
		width, height := req.BannerWidth, req.BannerHeight
		if width <= 0 && len(best.BannerSizes) > 0 {
			width, height = best.BannerSizes[0].Width, best.BannerSizes[0].Height
		}
		adMarkup := fmt.Sprintf("<iframe src=\"%s/render/v1/%s\" width=\"%d\" height=\"%d\" frameborder=\"0\" scrolling=\"no\"></iframe>", baseURL, crid, width, height)

		return Response{
			RequestID:             req.ID,
			NoBid:                 false,
			Candidate:             true,
			CandidateLineItemID:   best.ID,
			CandidateBidCPMMicros: best.BidCPMMicros,
			BidID:                 bidID,
			ImpressionID:          impID,
			Price:                 price,
			AdMarkup:              adMarkup,
			CreativeID:            crid,
			CampaignID:            best.CampaignID,
			Currency:              best.Currency,
			Width:                 width,
			Height:                height,
			ReservationID:         res.ID,
			ReservationToken:      token,
			WinNoticeURL:          nurl,
			BillableNoticeURL:     burl,
			LossNoticeURL:         lurl,
		}, nil
	}
	return noBid(req.ID), nil
}

func currencyMatches(lineItemCurrency string, requestCurrencies []string) bool {
	if lineItemCurrency == "" {
		return true // In-memory fixtures predating currency-aware manifests.
	}
	for _, currency := range requestCurrencies {
		if currency == lineItemCurrency {
			return true
		}
	}
	return false
}

func withinSchedule(now, startsAt time.Time, endsAt *time.Time) bool {
	if !startsAt.IsZero() && now.Before(startsAt) {
		return false
	}
	return endsAt == nil || now.Before(*endsAt)
}

func matchesTargeting(lineItem domain.LineItem, siteDomain, sitePage string, width, height int) bool {
	if len(lineItem.SiteDomains) > 0 {
		matched := false
		for _, candidate := range lineItem.SiteDomains {
			if normalizeDomain(candidate) == siteDomain {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	} else if lineItem.SiteDomain != "" && normalizeDomain(lineItem.SiteDomain) != siteDomain {
		return false
	}
	if len(lineItem.PagePathPrefixes) > 0 {
		pagePath := sitePage
		if parsed, err := url.Parse(sitePage); err == nil && parsed.Scheme != "" {
			pagePath = parsed.EscapedPath()
		}
		matched := false
		for _, prefix := range lineItem.PagePathPrefixes {
			if strings.HasPrefix(pagePath, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	} else if lineItem.SitePage != "" && lineItem.SitePage != sitePage {
		return false
	}
	if len(lineItem.BannerSizes) > 0 {
		matched := false
		for _, size := range lineItem.BannerSizes {
			if size.Width == width && size.Height == height {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	} else if (lineItem.BannerWidth > 0 || lineItem.BannerHeight > 0) && (lineItem.BannerWidth != width || lineItem.BannerHeight != height) {
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
