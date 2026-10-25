package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/snapshot"
)

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// LoadPublishedManifest reads the single current projection and never reads draft tables.
func (s *SQLStore) LoadPublishedManifest(ctx context.Context) (*snapshot.Manifest, error) {
	return s.loadPublishedManifest(ctx, s.db)
}

func (s *SQLStore) loadPublishedManifest(ctx context.Context, query queryContext) (*snapshot.Manifest, error) {
	rows, err := query.QueryContext(ctx, `SELECT aggregate_type, aggregate_id, config_version, payload
		FROM dsp_published_config WHERE tenant_id = ? ORDER BY aggregate_type, aggregate_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("read published DSP projection: %w", err)
	}
	defer rows.Close()
	manifest := &snapshot.Manifest{Campaigns: map[string]domain.Campaign{}, LineItems: map[string]domain.LineItem{}, Creatives: map[string]domain.Creative{}}
	var latestVersion string
	for rows.Next() {
		var kind string
		var id, payload []byte
		var version uint64
		if err := rows.Scan(&kind, &id, &version, &payload); err != nil {
			return nil, fmt.Errorf("scan published DSP projection: %w", err)
		}
		idText, versionText := hex.EncodeToString(id), fmt.Sprintf("%020d", version)
		if versionText > latestVersion {
			latestVersion = versionText
		}
		switch kind {
		case "campaign":
			var value domain.Campaign
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published campaign: %w", err)
			}
			value.ID = idText
			manifest.Campaigns[idText] = value
		case "line_item":
			var value domain.LineItem
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published line item: %w", err)
			}
			value.ID = idText
			manifest.LineItems[idText] = value
		case "creative":
			var value domain.Creative
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published creative: %w", err)
			}
			value.ID = idText
			manifest.Creatives[idText] = value
		default:
			return nil, fmt.Errorf("unknown published DSP resource type %q", kind)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published DSP projection: %w", err)
	}
	if latestVersion == "" {
		return nil, errors.New("no published DSP configuration")
	}
	for lineID, line := range manifest.LineItems {
		campaign, ok := manifest.Campaigns[line.CampaignID]
		if !ok {
			return nil, fmt.Errorf("published line item %q has no published campaign", lineID)
		}
		campaign.LineItemIDs = append(campaign.LineItemIDs, lineID)
		manifest.Campaigns[line.CampaignID] = campaign
	}
	for campaignID, campaign := range manifest.Campaigns {
		sort.Strings(campaign.LineItemIDs)
		manifest.Campaigns[campaignID] = campaign
	}
	manifest.Version = latestVersion
	if err := snapshot.Validate(manifest); err != nil {
		return nil, fmt.Errorf("validate published DSP manifest: %w", err)
	}
	return manifest, nil
}

// RefreshPublished loads and atomically activates the current projection.
func (s *SQLStore) RefreshPublished(ctx context.Context) error {
	manifest, err := s.LoadPublishedManifest(ctx)
	if err != nil {
		return err
	}
	if err := snapshot.Activate(manifest); err != nil {
		return fmt.Errorf("activate published DSP manifest: %w", err)
	}
	return nil
}

func (s *SQLStore) publishedPayload(ctx context.Context, tx *sql.Tx, kind string, id []byte) ([]byte, error) {
	switch kind {
	case "campaign":
		var status, advertiserStatus string
		var startsAt time.Time
		var endsAt sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT c.status, a.status, c.starts_at, c.ends_at FROM campaign c JOIN advertiser a
			ON a.tenant_id = c.tenant_id AND a.advertiser_id = c.advertiser_id
			WHERE c.tenant_id = ? AND c.campaign_id = ?`, s.tenantID, id).Scan(&status, &advertiserStatus, &startsAt, &endsAt); err != nil {
			return nil, err
		}
		campaign := domain.Campaign{ID: hex.EncodeToString(id), Active: status == "active" && advertiserStatus == "active", StartsAt: startsAt}
		if endsAt.Valid {
			campaign.EndsAt = &endsAt.Time
		}
		return json.Marshal(campaign)
	case "line_item":
		var campaignID []byte
		var status, currency string
		var bid, daily, lifetime int64
		var startsAt, endsAt sql.NullTime
		var targeting []byte
		if err := tx.QueryRowContext(ctx, `SELECT campaign_id, status, bid_cpm_micros, currency, daily_budget_micros, lifetime_budget_micros, starts_at, ends_at FROM line_item WHERE tenant_id = ? AND line_item_id = ?`, s.tenantID, id).Scan(&campaignID, &status, &bid, &currency, &daily, &lifetime, &startsAt, &endsAt); err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT targeting_spec FROM line_item_targeting_version WHERE tenant_id = ? AND line_item_id = ? ORDER BY targeting_version DESC LIMIT 1`, s.tenantID, id).Scan(&targeting); err != nil {
			return nil, err
		}
		var target Targeting
		if err := json.Unmarshal(targeting, &target); err != nil {
			return nil, err
		}
		var creatives []string
		rows, err := tx.QueryContext(ctx, `SELECT creative_id FROM line_item_creative WHERE tenant_id = ? AND line_item_id = ? ORDER BY creative_id`, s.tenantID, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var creativeID []byte
			if err := rows.Scan(&creativeID); err != nil {
				rows.Close()
				return nil, err
			}
			creatives = append(creatives, hex.EncodeToString(creativeID))
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		value := domain.LineItem{ID: hex.EncodeToString(id), CampaignID: hex.EncodeToString(campaignID), CreativeIDs: creatives, Active: status == "active", BidCPMMicros: bid, Currency: currency}
		if startsAt.Valid {
			value.StartsAt = &startsAt.Time
		}
		if endsAt.Valid {
			value.EndsAt = &endsAt.Time
		}
		value.SiteDomains = append([]string(nil), target.SiteDomains...)
		value.PagePathPrefixes = append([]string(nil), target.PagePathPrefixes...)
		for _, size := range target.BannerSizes {
			value.BannerSizes = append(value.BannerSizes, domain.BannerSize{Width: size.Width, Height: size.Height})
		}
		if len(target.SiteDomains) > 0 {
			value.SiteDomain = target.SiteDomains[0]
		}
		if len(target.PagePathPrefixes) > 0 {
			value.SitePage = target.PagePathPrefixes[0]
		}
		if len(target.BannerSizes) > 0 {
			value.BannerWidth, value.BannerHeight = target.BannerSizes[0].Width, target.BannerSizes[0].Height
		}
		return json.Marshal(value)
	case "creative":
		var status, format, assetRef, landing string
		var hash []byte
		var width, height, size int64
		if err := tx.QueryRowContext(ctx, `SELECT c.status, c.format, c.asset_ref, c.asset_sha256, c.asset_width_px, c.asset_height_px, c.landing_url, a.content_size_bytes FROM creative c JOIN dsp_creative_asset_registry a ON a.tenant_id = c.tenant_id AND a.asset_ref = c.asset_ref WHERE c.tenant_id = ? AND c.creative_id = ?`, s.tenantID, id).Scan(&status, &format, &assetRef, &hash, &width, &height, &landing, &size); err != nil {
			return nil, err
		}
		u, _ := url.Parse(landing)
		return json.Marshal(domain.Creative{ID: hex.EncodeToString(id), Active: status == "active", AssetMeta: domain.AssetMeta{MIMEType: format, SizeKB: int((size + 1023) / 1024), Hash: hex.EncodeToString(hash), Host: strings.ToLower(u.Hostname())}})
	default:
		return nil, invalid("resource_type", "is not publishable")
	}
}
