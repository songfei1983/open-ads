package control

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/assets"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

var ErrNotFound = errors.New("control resource not found")

// SQLStore persists management resources in the existing DSP schema.
type SQLStore struct {
	db         *sql.DB
	tenantID   []byte
	assetStore *assets.FileStore
}

// SetAssetStore attaches the immutable file store used for uploads and publish checks.
func (s *SQLStore) SetAssetStore(assetStore *assets.FileStore) {
	s.assetStore = assetStore
}

// UploadAsset stores validated bytes and registers their server-derived metadata.
func (s *SQLStore) UploadAsset(ctx context.Context, source io.Reader, expectedSHA256 string) (assets.Metadata, error) {
	if s.assetStore == nil {
		return assets.Metadata{}, errors.New("asset store is not configured")
	}
	tenantID := hex.EncodeToString(s.tenantID)
	metadata, err := s.assetStore.Put(ctx, tenantID, source, expectedSHA256)
	if err != nil {
		return assets.Metadata{}, err
	}
	digest, err := hex.DecodeString(metadata.SHA256)
	if err != nil {
		return assets.Metadata{}, fmt.Errorf("decode verified asset hash: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO dsp_creative_asset_registry
		(tenant_id, asset_ref, asset_sha256, content_size_bytes, width_px, height_px, mime_type, uploaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE asset_ref = VALUES(asset_ref)`,
		s.tenantID, metadata.AssetRef, digest, metadata.SizeBytes, metadata.Width, metadata.Height, metadata.MIMEType, time.Now().UTC())
	if err != nil {
		return assets.Metadata{}, fmt.Errorf("register uploaded asset: %w", err)
	}
	registered, err := s.loadAssetMetadata(ctx, metadata.AssetRef)
	if err != nil {
		return assets.Metadata{}, err
	}
	if registered.SHA256 != metadata.SHA256 || registered.SizeBytes != metadata.SizeBytes ||
		registered.Width != metadata.Width || registered.Height != metadata.Height || registered.MIMEType != metadata.MIMEType {
		return assets.Metadata{}, assets.ErrAssetTampered
	}
	return metadata, nil
}

func (s *SQLStore) loadAssetMetadata(ctx context.Context, assetRef string) (assets.Metadata, error) {
	tenantID := hex.EncodeToString(s.tenantID)
	refTenant, _, err := assets.ParseAssetRef(assetRef)
	if err != nil || refTenant != tenantID {
		return assets.Metadata{}, assets.ErrInvalidAssetRef
	}
	var digest []byte
	var metadata assets.Metadata
	err = s.db.QueryRowContext(ctx, `SELECT asset_sha256, content_size_bytes, width_px, height_px, mime_type
		FROM dsp_creative_asset_registry WHERE tenant_id = ? AND asset_ref = ?`, s.tenantID, assetRef).Scan(
		&digest, &metadata.SizeBytes, &metadata.Width, &metadata.Height, &metadata.MIMEType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return assets.Metadata{}, assets.ErrAssetNotFound
		}
		return assets.Metadata{}, fmt.Errorf("read creative asset metadata: %w", err)
	}
	metadata.AssetRef, metadata.SHA256 = assetRef, hex.EncodeToString(digest)
	return metadata, nil
}

// NewSQLStore creates a tenant-scoped repository.
func NewSQLStore(db *sql.DB, tenantID string) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	tenant, err := uuidBytes(tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant id: %w", err)
	}
	return &SQLStore{db: db, tenantID: tenant}, nil
}

func (s *SQLStore) ListAdvertisers(ctx context.Context) ([]Advertiser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.advertiser_id, a.status, a.default_currency, a.version
		FROM advertiser a WHERE a.tenant_id = ? ORDER BY a.advertiser_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list advertisers: %w", err)
	}
	defer rows.Close()

	items := []Advertiser{}
	for rows.Next() {
		var id []byte
		var item Advertiser
		if err := rows.Scan(&id, &item.Status, &item.DefaultCurrency, &item.Version); err != nil {
			return nil, fmt.Errorf("scan advertiser: %w", err)
		}
		item.ID = hex.EncodeToString(id)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate advertisers: %w", err)
	}
	return items, nil
}

func (s *SQLStore) CreateAdvertiser(ctx context.Context, item Advertiser) (Advertiser, error) {
	if err := item.Validate(); err != nil {
		return Advertiser{}, err
	}
	id, err := newID(item.ID)
	if err != nil {
		return Advertiser{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO advertiser
		(tenant_id, advertiser_id, status, default_currency, version, updated_at)
		VALUES (?, ?, ?, ?, 1, ?)`, s.tenantID, id, item.Status, item.DefaultCurrency, time.Now().UTC())
	if err != nil {
		return Advertiser{}, fmt.Errorf("create advertiser: %w", err)
	}
	item.ID, item.Version = hex.EncodeToString(id), 1
	return item, nil
}

func (s *SQLStore) UpdateAdvertiser(ctx context.Context, id string, item Advertiser) (Advertiser, error) {
	if item.Version == 0 {
		return Advertiser{}, invalid("version", "must be positive")
	}
	if err := item.Validate(); err != nil {
		return Advertiser{}, err
	}
	resourceID, err := uuidBytes(id)
	if err != nil {
		return Advertiser{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Advertiser{}, fmt.Errorf("begin advertiser update: %w", err)
	}
	defer tx.Rollback()
	version, err := lockVersion(ctx, tx, `SELECT version FROM advertiser WHERE tenant_id = ? AND advertiser_id = ? FOR UPDATE`, s.tenantID, resourceID)
	if err != nil {
		return Advertiser{}, err
	}
	if version != item.Version {
		return Advertiser{}, ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE advertiser SET status = ?, default_currency = ?, version = version + 1, updated_at = ?
		WHERE tenant_id = ? AND advertiser_id = ?`, item.Status, item.DefaultCurrency, time.Now().UTC(), s.tenantID, resourceID)
	if err != nil {
		return Advertiser{}, fmt.Errorf("update advertiser: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Advertiser{}, fmt.Errorf("commit advertiser update: %w", err)
	}
	item.ID, item.Version = id, version+1
	return item, nil
}

func (s *SQLStore) ListCampaigns(ctx context.Context) ([]Campaign, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.campaign_id, c.advertiser_id, c.status, c.starts_at, c.ends_at, c.version,
		(SELECT CONCAT(HEX(o.event_id), ':', IF(o.published_at IS NULL, 'pending', 'published'))
		FROM dsp_config_outbox o WHERE o.tenant_id = c.tenant_id AND o.aggregate_type = 'campaign'
		AND o.aggregate_id = c.campaign_id AND o.aggregate_version = c.version ORDER BY o.created_at DESC LIMIT 1)
		FROM campaign c WHERE c.tenant_id = ? ORDER BY c.campaign_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	defer rows.Close()
	items := []Campaign{}
	for rows.Next() {
		var id, advertiserID []byte
		var item Campaign
		var endsAt sql.NullTime
		var publishedVersion sql.NullString
		if err := rows.Scan(&id, &advertiserID, &item.Status, &item.StartsAt, &endsAt, &item.Version, &publishedVersion); err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		item.ID, item.AdvertiserID = hex.EncodeToString(id), hex.EncodeToString(advertiserID)
		item.StartsAt = item.StartsAt.UTC()
		if endsAt.Valid {
			value := endsAt.Time.UTC()
			item.EndsAt = &value
		}
		setPublishState(&item.ConfigVersion, &item.PublishStatus, publishedVersion)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaigns: %w", err)
	}
	return items, nil
}

func (s *SQLStore) CreateCampaign(ctx context.Context, item Campaign) (Campaign, error) {
	if err := item.Validate(); err != nil {
		return Campaign{}, err
	}
	id, err := newID(item.ID)
	if err != nil {
		return Campaign{}, err
	}
	advertiserID, err := uuidBytes(item.AdvertiserID)
	if err != nil {
		return Campaign{}, invalid("advertiser_id", "must be a 32-character lowercase UUID")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO campaign
		(tenant_id, campaign_id, advertiser_id, status, starts_at, ends_at, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)`, s.tenantID, id, advertiserID, item.Status, item.StartsAt.UTC(), nullableTime(item.EndsAt), time.Now().UTC())
	if err != nil {
		return Campaign{}, fmt.Errorf("create campaign: %w", err)
	}
	item.ID, item.Version = hex.EncodeToString(id), 1
	item.PublishStatus = "draft"
	return item, nil
}

func (s *SQLStore) UpdateCampaign(ctx context.Context, id string, item Campaign) (Campaign, error) {
	if item.Version == 0 {
		return Campaign{}, invalid("version", "must be positive")
	}
	if err := item.Validate(); err != nil {
		return Campaign{}, err
	}
	resourceID, err := uuidBytes(id)
	if err != nil {
		return Campaign{}, err
	}
	advertiserID, err := uuidBytes(item.AdvertiserID)
	if err != nil {
		return Campaign{}, invalid("advertiser_id", "must be a 32-character lowercase UUID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Campaign{}, fmt.Errorf("begin campaign update: %w", err)
	}
	defer tx.Rollback()
	version, err := lockVersion(ctx, tx, `SELECT version FROM campaign WHERE tenant_id = ? AND campaign_id = ? FOR UPDATE`, s.tenantID, resourceID)
	if err != nil {
		return Campaign{}, err
	}
	if version != item.Version {
		return Campaign{}, ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE campaign SET advertiser_id = ?, status = ?, starts_at = ?, ends_at = ?, version = version + 1, updated_at = ?
		WHERE tenant_id = ? AND campaign_id = ?`, advertiserID, item.Status, item.StartsAt.UTC(), nullableTime(item.EndsAt), time.Now().UTC(), s.tenantID, resourceID)
	if err != nil {
		return Campaign{}, fmt.Errorf("update campaign: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Campaign{}, fmt.Errorf("commit campaign update: %w", err)
	}
	item.ID, item.Version = id, version+1
	item.ConfigVersion, item.PublishStatus = "", "draft"
	return item, nil
}

func (s *SQLStore) ListCreatives(ctx context.Context) ([]Creative, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.creative_id, c.status, c.format, c.asset_ref, c.asset_sha256,
		c.asset_width_px, c.asset_height_px, c.landing_url, c.version,
		(SELECT CONCAT(HEX(o.event_id), ':', IF(o.published_at IS NULL, 'pending', 'published'))
		FROM dsp_config_outbox o WHERE o.tenant_id = c.tenant_id AND o.aggregate_type = 'creative'
		AND o.aggregate_id = c.creative_id AND o.aggregate_version = c.version ORDER BY o.created_at DESC LIMIT 1)
		FROM creative c WHERE c.tenant_id = ? ORDER BY c.creative_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list creatives: %w", err)
	}
	defer rows.Close()
	items := []Creative{}
	for rows.Next() {
		var id, digest []byte
		var item Creative
		var publishedVersion sql.NullString
		if err := rows.Scan(&id, &item.Status, &item.Format, &item.AssetRef, &digest,
			&item.Width, &item.Height, &item.LandingURL, &item.Version, &publishedVersion); err != nil {
			return nil, fmt.Errorf("scan creative: %w", err)
		}
		item.ID, item.AssetSHA256 = hex.EncodeToString(id), hex.EncodeToString(digest)
		setPublishState(&item.ConfigVersion, &item.PublishStatus, publishedVersion)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate creatives: %w", err)
	}
	return items, nil
}

func (s *SQLStore) CreateCreative(ctx context.Context, input CreativeInput) (Creative, error) {
	if err := input.Validate(); err != nil {
		return Creative{}, err
	}
	id, err := newID(input.ID)
	if err != nil {
		return Creative{}, err
	}
	metadata, err := s.loadAssetMetadata(ctx, input.AssetRef)
	if err != nil {
		return Creative{}, err
	}
	if s.assetStore == nil {
		return Creative{}, errors.New("asset store is not configured")
	}
	if err := s.assetStore.Verify(ctx, hex.EncodeToString(s.tenantID), metadata.AssetRef, metadata.SHA256); err != nil {
		return Creative{}, err
	}
	digest, err := hex.DecodeString(metadata.SHA256)
	if err != nil {
		return Creative{}, fmt.Errorf("decode registered asset hash: %w", err)
	}
	item := Creative{Status: input.Status, Format: metadata.MIMEType, AssetRef: metadata.AssetRef,
		AssetSHA256: metadata.SHA256, Width: metadata.Width, Height: metadata.Height, LandingURL: input.LandingURL}
	if err := item.Validate(); err != nil {
		return Creative{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO creative
		(tenant_id, creative_id, status, format, asset_ref, asset_sha256, asset_width_px, asset_height_px, landing_url, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`, s.tenantID, id, item.Status, item.Format, item.AssetRef,
		digest, item.Width, item.Height, item.LandingURL, time.Now().UTC())
	if err != nil {
		return Creative{}, fmt.Errorf("create creative: %w", err)
	}
	item.ID, item.Version = hex.EncodeToString(id), 1
	item.PublishStatus = "draft"
	return item, nil
}

func (s *SQLStore) UpdateCreative(ctx context.Context, id string, input CreativeInput) (Creative, error) {
	if input.Version == 0 {
		return Creative{}, invalid("version", "must be positive")
	}
	if err := input.Validate(); err != nil {
		return Creative{}, err
	}
	resourceID, err := uuidBytes(id)
	if err != nil {
		return Creative{}, err
	}
	metadata, err := s.loadAssetMetadata(ctx, input.AssetRef)
	if err != nil {
		return Creative{}, err
	}
	if s.assetStore == nil {
		return Creative{}, errors.New("asset store is not configured")
	}
	if err := s.assetStore.Verify(ctx, hex.EncodeToString(s.tenantID), metadata.AssetRef, metadata.SHA256); err != nil {
		return Creative{}, err
	}
	digest, err := hex.DecodeString(metadata.SHA256)
	if err != nil {
		return Creative{}, fmt.Errorf("decode registered asset hash: %w", err)
	}
	item := Creative{Status: input.Status, Format: metadata.MIMEType, AssetRef: metadata.AssetRef,
		AssetSHA256: metadata.SHA256, Width: metadata.Width, Height: metadata.Height,
		LandingURL: input.LandingURL, Version: input.Version}
	if err := item.Validate(); err != nil {
		return Creative{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Creative{}, fmt.Errorf("begin creative update: %w", err)
	}
	defer tx.Rollback()
	version, err := lockVersion(ctx, tx, `SELECT version FROM creative WHERE tenant_id = ? AND creative_id = ? FOR UPDATE`, s.tenantID, resourceID)
	if err != nil {
		return Creative{}, err
	}
	if version != input.Version {
		return Creative{}, ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE creative SET status = ?, format = ?, asset_ref = ?, asset_sha256 = ?,
		asset_width_px = ?, asset_height_px = ?, landing_url = ?, version = version + 1, updated_at = ?
		WHERE tenant_id = ? AND creative_id = ?`, item.Status, item.Format, item.AssetRef, digest,
		item.Width, item.Height, item.LandingURL, time.Now().UTC(), s.tenantID, resourceID)
	if err != nil {
		return Creative{}, fmt.Errorf("update creative: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Creative{}, fmt.Errorf("commit creative update: %w", err)
	}
	item.ID, item.Version = id, version+1
	item.ConfigVersion, item.PublishStatus = "", "draft"
	return item, nil
}

func (s *SQLStore) ListLineItems(ctx context.Context) ([]LineItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT li.line_item_id, li.campaign_id, li.status, li.bid_cpm_micros,
		li.currency, li.daily_budget_micros, li.lifetime_budget_micros, li.starts_at, li.ends_at,
		li.version, tiv.targeting_spec,
		(SELECT CONCAT(HEX(o.event_id), ':', IF(o.published_at IS NULL, 'pending', 'published'))
		FROM dsp_config_outbox o WHERE o.tenant_id = li.tenant_id AND o.aggregate_type = 'line_item'
		AND o.aggregate_id = li.line_item_id AND o.aggregate_version = li.version ORDER BY o.created_at DESC LIMIT 1)
		FROM line_item li JOIN line_item_targeting_version tiv
		ON tiv.tenant_id = li.tenant_id AND tiv.line_item_id = li.line_item_id AND tiv.targeting_version = li.targeting_version
		WHERE li.tenant_id = ? ORDER BY li.line_item_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list line items: %w", err)
	}
	defer rows.Close()
	items := []LineItem{}
	for rows.Next() {
		var id, campaignID, targetingJSON []byte
		var item LineItem
		var startsAt, endsAt sql.NullTime
		var publishedVersion sql.NullString
		if err := rows.Scan(&id, &campaignID, &item.Status, &item.BidCPMMicros, &item.Currency,
			&item.DailyBudgetMicros, &item.LifetimeBudgetMicros, &startsAt, &endsAt,
			&item.Version, &targetingJSON, &publishedVersion); err != nil {
			return nil, fmt.Errorf("scan line item: %w", err)
		}
		item.ID, item.CampaignID = hex.EncodeToString(id), hex.EncodeToString(campaignID)
		if startsAt.Valid {
			value := startsAt.Time.UTC()
			item.StartsAt = &value
		}
		if endsAt.Valid {
			value := endsAt.Time.UTC()
			item.EndsAt = &value
		}
		if err := json.Unmarshal(targetingJSON, &item.Targeting); err != nil {
			return nil, fmt.Errorf("decode line item targeting: %w", err)
		}
		setPublishState(&item.ConfigVersion, &item.PublishStatus, publishedVersion)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate line items: %w", err)
	}
	if len(items) == 0 {
		return items, nil
	}

	links, err := s.db.QueryContext(ctx, `SELECT line_item_id, creative_id FROM line_item_creative
		WHERE tenant_id = ? ORDER BY line_item_id, creative_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list line item creatives: %w", err)
	}
	defer links.Close()
	byID := make(map[string]int, len(items))
	for i := range items {
		byID[items[i].ID] = i
		items[i].CreativeIDs = []string{}
	}
	for links.Next() {
		var lineItemID, creativeID []byte
		if err := links.Scan(&lineItemID, &creativeID); err != nil {
			return nil, fmt.Errorf("scan line item creative: %w", err)
		}
		if i, ok := byID[hex.EncodeToString(lineItemID)]; ok {
			items[i].CreativeIDs = append(items[i].CreativeIDs, hex.EncodeToString(creativeID))
		}
	}
	if err := links.Err(); err != nil {
		return nil, fmt.Errorf("iterate line item creatives: %w", err)
	}
	return items, nil
}

func (s *SQLStore) CreateLineItem(ctx context.Context, item LineItem) (LineItem, error) {
	if err := item.Validate(); err != nil {
		return LineItem{}, err
	}
	id, err := newID(item.ID)
	if err != nil {
		return LineItem{}, err
	}
	campaignID, err := uuidBytes(item.CampaignID)
	if err != nil {
		return LineItem{}, invalid("campaign_id", "must be a 32-character lowercase UUID")
	}
	targetingJSON, err := encodeTargeting(item.Targeting)
	if err != nil {
		return LineItem{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LineItem{}, fmt.Errorf("begin line item creation: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO line_item
		(tenant_id, line_item_id, campaign_id, status, starts_at, ends_at, currency, bid_cpm_micros,
		daily_budget_micros, lifetime_budget_micros, targeting_version, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?)`, s.tenantID, id, campaignID, item.Status,
		nullableTime(item.StartsAt), nullableTime(item.EndsAt), item.Currency, item.BidCPMMicros,
		item.DailyBudgetMicros, item.LifetimeBudgetMicros, time.Now().UTC())
	if err != nil {
		return LineItem{}, fmt.Errorf("create line item: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO line_item_targeting_version
		(tenant_id, line_item_id, targeting_version, targeting_spec, created_at) VALUES (?, ?, 1, ?, ?)`,
		s.tenantID, id, targetingJSON, time.Now().UTC())
	if err != nil {
		return LineItem{}, fmt.Errorf("create line item targeting: %w", err)
	}
	if err := replaceCreativeLinks(ctx, tx, s.tenantID, id, item.CreativeIDs); err != nil {
		return LineItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return LineItem{}, fmt.Errorf("commit line item creation: %w", err)
	}
	item.ID, item.Version = hex.EncodeToString(id), 1
	item.PublishStatus = "draft"
	return item, nil
}

func (s *SQLStore) UpdateLineItem(ctx context.Context, id string, item LineItem) (LineItem, error) {
	if item.Version == 0 {
		return LineItem{}, invalid("version", "must be positive")
	}
	if err := item.Validate(); err != nil {
		return LineItem{}, err
	}
	resourceID, err := uuidBytes(id)
	if err != nil {
		return LineItem{}, err
	}
	campaignID, err := uuidBytes(item.CampaignID)
	if err != nil {
		return LineItem{}, invalid("campaign_id", "must be a 32-character lowercase UUID")
	}
	targetingJSON, err := encodeTargeting(item.Targeting)
	if err != nil {
		return LineItem{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LineItem{}, fmt.Errorf("begin line item update: %w", err)
	}
	defer tx.Rollback()
	var version, targetingVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT version, targeting_version FROM line_item
		WHERE tenant_id = ? AND line_item_id = ? FOR UPDATE`, s.tenantID, resourceID).Scan(&version, &targetingVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LineItem{}, ErrNotFound
		}
		return LineItem{}, fmt.Errorf("read line item version: %w", err)
	}
	if version != item.Version {
		return LineItem{}, ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE line_item SET campaign_id = ?, status = ?, starts_at = ?, ends_at = ?,
		currency = ?, bid_cpm_micros = ?, daily_budget_micros = ?, lifetime_budget_micros = ?,
		targeting_version = ?, version = version + 1, updated_at = ?
		WHERE tenant_id = ? AND line_item_id = ?`, campaignID, item.Status, nullableTime(item.StartsAt), nullableTime(item.EndsAt),
		item.Currency, item.BidCPMMicros, item.DailyBudgetMicros, item.LifetimeBudgetMicros,
		targetingVersion+1, time.Now().UTC(), s.tenantID, resourceID)
	if err != nil {
		return LineItem{}, fmt.Errorf("update line item: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO line_item_targeting_version
		(tenant_id, line_item_id, targeting_version, targeting_spec, created_at) VALUES (?, ?, ?, ?, ?)`,
		s.tenantID, resourceID, targetingVersion+1, targetingJSON, time.Now().UTC())
	if err != nil {
		return LineItem{}, fmt.Errorf("update line item targeting: %w", err)
	}
	if err := replaceCreativeLinks(ctx, tx, s.tenantID, resourceID, item.CreativeIDs); err != nil {
		return LineItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return LineItem{}, fmt.Errorf("commit line item update: %w", err)
	}
	item.ID, item.Version = id, version+1
	item.ConfigVersion, item.PublishStatus = "", "draft"
	return item, nil
}

func replaceCreativeLinks(ctx context.Context, tx *sql.Tx, tenantID, lineItemID []byte, creativeIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM line_item_creative WHERE tenant_id = ? AND line_item_id = ?`, tenantID, lineItemID); err != nil {
		return fmt.Errorf("remove line item creative links: %w", err)
	}
	for _, creativeID := range creativeIDs {
		id, err := uuidBytes(creativeID)
		if err != nil {
			return invalid("creative_ids", "must contain valid UUIDs")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO line_item_creative (tenant_id, line_item_id, creative_id) VALUES (?, ?, ?)`, tenantID, lineItemID, id); err != nil {
			return fmt.Errorf("link line item creative: %w", err)
		}
	}
	return nil
}

// Publish validates a resource version and atomically updates its current
// published projection together with the corresponding outbox event.
func (s *SQLStore) Publish(ctx context.Context, resourceType, id string, expectedVersion uint64) (string, error) {
	resourceID, err := uuidBytes(id)
	if err != nil {
		return "", err
	}
	if expectedVersion == 0 {
		return "", invalid("version", "must be positive")
	}
	if resourceType != "campaign" && resourceType != "line_item" && resourceType != "creative" {
		return "", invalid("resource_type", "is not publishable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin publish: %w", err)
	}
	defer tx.Rollback()
	table, keyColumn := publishTable(resourceType)
	version, err := lockVersion(ctx, tx, "SELECT version FROM "+table+" WHERE tenant_id = ? AND "+keyColumn+" = ? FOR UPDATE", s.tenantID, resourceID)
	if err != nil {
		return "", err
	}
	if version != expectedVersion {
		return "", ErrVersionConflict
	}
	if err := validatePublish(ctx, tx, s.tenantID, resourceType, resourceID, s.assetStore); err != nil {
		return "", err
	}
	configVersion, err := domain.NewUUIDv7()
	if err != nil {
		return "", fmt.Errorf("generate config version: %w", err)
	}
	configVersionBytes, _ := hex.DecodeString(configVersion)
	if _, err := tx.ExecContext(ctx, `INSERT INTO dsp_config_manifest_clock (tenant_id, manifest_version)
		VALUES (?, 1) ON DUPLICATE KEY UPDATE manifest_version = manifest_version + 1`, s.tenantID); err != nil {
		return "", fmt.Errorf("advance tenant manifest version: %w", err)
	}
	var manifestVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT manifest_version FROM dsp_config_manifest_clock WHERE tenant_id = ? FOR UPDATE`, s.tenantID).Scan(&manifestVersion); err != nil {
		return "", fmt.Errorf("read tenant manifest version: %w", err)
	}
	payload, err := s.publishedPayload(ctx, tx, resourceType, resourceID)
	if err != nil {
		return "", fmt.Errorf("build published configuration projection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO dsp_published_config
		(tenant_id, aggregate_type, aggregate_id, aggregate_version, config_version, payload, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE
		aggregate_version = VALUES(aggregate_version), config_version = VALUES(config_version),
		payload = VALUES(payload), updated_at = VALUES(updated_at)`,
		s.tenantID, resourceType, resourceID, expectedVersion, manifestVersion, payload, time.Now().UTC()); err != nil {
		return "", fmt.Errorf("update published configuration projection: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dsp_config_outbox
		(event_id, tenant_id, aggregate_type, aggregate_id, aggregate_version, created_at, published_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)`, configVersionBytes, s.tenantID, resourceType, resourceID,
		expectedVersion, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("record publish event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit publish: %w", err)
	}
	return configVersion, nil
}

func setPublishState(configVersion, publishStatus *string, publishedVersion sql.NullString) {
	*publishStatus = "draft"
	if !publishedVersion.Valid {
		return
	}
	value, status, ok := strings.Cut(publishedVersion.String, ":")
	if !ok {
		return
	}
	*configVersion = strings.ToLower(value)
	*publishStatus = status
}

func publishTable(resourceType string) (string, string) {
	switch resourceType {
	case "campaign":
		return "campaign", "campaign_id"
	case "line_item":
		return "line_item", "line_item_id"
	default:
		return "creative", "creative_id"
	}
}

func validatePublish(ctx context.Context, tx *sql.Tx, tenantID []byte, resourceType string, id []byte, assetStore *assets.FileStore) error {
	switch resourceType {
	case "campaign":
		var advertiserID []byte
		var status string
		var startsAt time.Time
		var endsAt sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT advertiser_id, status, starts_at, ends_at FROM campaign
			WHERE tenant_id = ? AND campaign_id = ?`, tenantID, id).Scan(&advertiserID, &status, &startsAt, &endsAt); err != nil {
			return fmt.Errorf("read campaign for publish: %w", err)
		}
		if status != "active" && status != "paused" || startsAt.IsZero() || endsAt.Valid && !endsAt.Time.After(startsAt) {
			return invalid("campaign", "must have a valid status and time window to publish")
		}
		var advertiserStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM advertiser WHERE tenant_id = ? AND advertiser_id = ?`, tenantID, advertiserID).Scan(&advertiserStatus); err != nil {
			return fmt.Errorf("read campaign advertiser: %w", err)
		}
		if status == "active" && advertiserStatus != "active" {
			return invalid("advertiser", "must be active to publish a campaign")
		}
	case "line_item":
		var item LineItem
		var campaignID []byte
		var startsAt, endsAt sql.NullTime
		var targetingJSON []byte
		if err := tx.QueryRowContext(ctx, `SELECT li.campaign_id, li.status, li.bid_cpm_micros, li.currency,
			li.daily_budget_micros, li.lifetime_budget_micros, li.starts_at, li.ends_at, tiv.targeting_spec
			FROM line_item li JOIN line_item_targeting_version tiv
			ON tiv.tenant_id = li.tenant_id AND tiv.line_item_id = li.line_item_id AND tiv.targeting_version = li.targeting_version
			WHERE li.tenant_id = ? AND li.line_item_id = ?`, tenantID, id).Scan(
			&campaignID, &item.Status, &item.BidCPMMicros, &item.Currency,
			&item.DailyBudgetMicros, &item.LifetimeBudgetMicros, &startsAt, &endsAt, &targetingJSON); err != nil {
			return fmt.Errorf("read line item for publish: %w", err)
		}
		if item.Status != "active" && item.Status != "paused" || item.BidCPMMicros <= 0 || item.DailyBudgetMicros <= 0 || item.LifetimeBudgetMicros <= 0 {
			return invalid("line_item", "must have a valid status and positive bid and budgets to publish")
		}
		if startsAt.Valid {
			value := startsAt.Time
			item.StartsAt = &value
		}
		if endsAt.Valid {
			value := endsAt.Time
			item.EndsAt = &value
		}
		if err := json.Unmarshal(targetingJSON, &item.Targeting); err != nil {
			return fmt.Errorf("decode line item targeting: %w", err)
		}
		if err := item.Targeting.Validate(); err != nil {
			return err
		}
		var campaignStatus string
		var campaignVersion uint64
		if err := tx.QueryRowContext(ctx, `SELECT status, version FROM campaign WHERE tenant_id = ? AND campaign_id = ?`, tenantID, campaignID).Scan(&campaignStatus, &campaignVersion); err != nil {
			return fmt.Errorf("read line item campaign: %w", err)
		}
		campaignPublished, err := isPublished(ctx, tx, tenantID, "campaign", campaignID, campaignVersion)
		if err != nil {
			return err
		}
		if item.Status == "active" && campaignStatus != "active" || !campaignPublished {
			return invalid("campaign_id", "must reference an active published campaign")
		}
		rows, err := tx.QueryContext(ctx, `SELECT c.creative_id, c.status, c.version FROM line_item_creative lic
			JOIN creative c ON c.tenant_id = lic.tenant_id AND c.creative_id = lic.creative_id
			WHERE lic.tenant_id = ? AND lic.line_item_id = ? ORDER BY c.creative_id`, tenantID, id)
		if err != nil {
			return fmt.Errorf("read line item creatives: %w", err)
		}
		count := 0
		for rows.Next() {
			var creativeID []byte
			var status string
			var version uint64
			if err := rows.Scan(&creativeID, &status, &version); err != nil {
				rows.Close()
				return fmt.Errorf("scan line item creative: %w", err)
			}
			published, err := isPublished(ctx, tx, tenantID, "creative", creativeID, version)
			if err != nil {
				rows.Close()
				return err
			}
			if item.Status == "active" && (status != "active" || !published) {
				rows.Close()
				return invalid("creative_ids", "active line items must reference active published creatives")
			}
			count++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate line item creatives: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close line item creatives: %w", err)
		}
		if count == 0 {
			return invalid("creative_ids", "at least one creative is required")
		}
		todayMidnight := time.Now().UTC().Truncate(24 * time.Hour)
		epochStart := time.Unix(0, 0).UTC()
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_account
			(tenant_id, line_item_id, budget_scope, period_start, currency, budget_limit_micros, spent_micros, leased_micros, version, updated_at)
			VALUES (?, ?, 'daily', ?, ?, ?, 0, 0, 1, NOW(6))
			ON DUPLICATE KEY UPDATE budget_limit_micros = VALUES(budget_limit_micros), updated_at = NOW(6)`,
			tenantID, id, todayMidnight, item.Currency, item.DailyBudgetMicros)
		if err != nil {
			return fmt.Errorf("upsert daily budget account: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_account
			(tenant_id, line_item_id, budget_scope, period_start, currency, budget_limit_micros, spent_micros, leased_micros, version, updated_at)
			VALUES (?, ?, 'lifetime', ?, ?, ?, 0, 0, 1, NOW(6))
			ON DUPLICATE KEY UPDATE budget_limit_micros = VALUES(budget_limit_micros), updated_at = NOW(6)`,
			tenantID, id, epochStart, item.Currency, item.LifetimeBudgetMicros)
		if err != nil {
			return fmt.Errorf("upsert lifetime budget account: %w", err)
		}
	case "creative":
		var item Creative
		var digest, registeredDigest []byte
		var registrySize int64
		var registryWidth, registryHeight int
		var registryMIME string
		if err := tx.QueryRowContext(ctx, `SELECT status, format, asset_ref, asset_sha256, asset_width_px,
			asset_height_px, landing_url FROM creative WHERE tenant_id = ? AND creative_id = ?`, tenantID, id).Scan(
			&item.Status, &item.Format, &item.AssetRef, &digest, &item.Width, &item.Height, &item.LandingURL); err != nil {
			return fmt.Errorf("read creative for publish: %w", err)
		}
		item.AssetSHA256 = hex.EncodeToString(digest)
		if err := item.Validate(); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT asset_sha256, content_size_bytes, width_px, height_px, mime_type
			FROM dsp_creative_asset_registry WHERE tenant_id = ? AND asset_ref = ?`, tenantID, item.AssetRef).Scan(
			&registeredDigest, &registrySize, &registryWidth, &registryHeight, &registryMIME); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return assets.ErrAssetNotFound
			}
			return fmt.Errorf("read creative asset registry for publish: %w", err)
		}
		if !bytes.Equal(registeredDigest, digest) || registryWidth != item.Width || registryHeight != item.Height || registryMIME != item.Format || registrySize < 1 {
			return assets.ErrAssetTampered
		}
		if assetStore == nil {
			return errors.New("asset store is not configured")
		}
		if err := assetStore.Verify(ctx, hex.EncodeToString(tenantID), item.AssetRef, item.AssetSHA256); err != nil {
			return err
		}
	}
	return nil
}

func isPublished(ctx context.Context, tx *sql.Tx, tenantID []byte, resourceType string, id []byte, version uint64) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM dsp_config_outbox WHERE tenant_id = ?
		AND aggregate_type = ? AND aggregate_id = ? AND aggregate_version = ?`, tenantID, resourceType, id, version).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check published resource: %w", err)
	}
	return count > 0, nil
}

func lockVersion(ctx context.Context, tx *sql.Tx, query string, args ...any) (uint64, error) {
	var version uint64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("read resource version: %w", err)
	}
	return version, nil
}

func uuidBytes(value string) ([]byte, error) {
	if err := domain.ValidateUUID32(value); err != nil || value != strings.ToLower(value) {
		return nil, domain.ErrInvalidUUID
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode uuid: %w", err)
	}
	return decoded, nil
}

func newID(value string) ([]byte, error) {
	if value == "" {
		generated, err := domain.NewUUIDv7()
		if err != nil {
			return nil, fmt.Errorf("generate resource id: %w", err)
		}
		value = generated
	}
	decoded, err := uuidBytes(value)
	if err != nil {
		return nil, invalid("id", "must be a 32-character lowercase UUID")
	}
	return decoded, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func encodeTargeting(value Targeting) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode targeting: %w", err)
	}
	return encoded, nil
}

func (s *SQLStore) ListBudgets(ctx context.Context) ([]BudgetSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT line_item_id, budget_scope, period_start, currency,
		budget_limit_micros, spent_micros, leased_micros
		FROM budget_account WHERE tenant_id = ? ORDER BY line_item_id, budget_scope`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	defer rows.Close()

	items := []BudgetSummary{}
	for rows.Next() {
		var liBytes []byte
		var scope, currency string
		var period time.Time
		var limit, spent, leased int64
		if err := rows.Scan(&liBytes, &scope, &period, &currency, &limit, &spent, &leased); err != nil {
			return nil, fmt.Errorf("scan budget: %w", err)
		}
		avail := limit - (spent + leased)
		if avail < 0 {
			avail = 0
		}
		status := "healthy"
		if avail == 0 {
			status = "depleted"
		}
		items = append(items, BudgetSummary{
			LineItemID:        hex.EncodeToString(liBytes),
			BudgetScope:       scope,
			PeriodStart:       period.Format(time.RFC3339),
			Currency:          currency,
			BudgetLimitMicros: limit,
			SpentMicros:       spent,
			LeasedMicros:      leased,
			AvailableMicros:   avail,
			Status:            status,
		})
	}
	return items, nil
}

func (s *SQLStore) ListQuarantine(ctx context.Context) ([]QuarantineRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT quarantine_id, reservation_id, source_event_key, outcome,
		reason_code, status, received_at
		FROM dsp_notice_quarantine WHERE tenant_id = ? ORDER BY received_at DESC LIMIT 100`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list quarantine: %w", err)
	}
	defer rows.Close()

	items := []QuarantineRecord{}
	for rows.Next() {
		var qBytes, resBytes, srcBytes []byte
		var outcome, reason, status string
		var receivedAt time.Time
		if err := rows.Scan(&qBytes, &resBytes, &srcBytes, &outcome, &reason, &status, &receivedAt); err != nil {
			return nil, fmt.Errorf("scan quarantine: %w", err)
		}
		items = append(items, QuarantineRecord{
			QuarantineID:   hex.EncodeToString(qBytes),
			ReservationID:  hex.EncodeToString(resBytes),
			SourceEventKey: hex.EncodeToString(srcBytes),
			Outcome:        outcome,
			ReasonCode:     reason,
			Status:         status,
			ReceivedAt:     receivedAt.Format(time.RFC3339),
		})
	}
	return items, nil
}

func (s *SQLStore) ResolveQuarantine(ctx context.Context, id string) error {
	qBytes, err := uuidBytes(id)
	if err != nil {
		return invalid("quarantine_id", "must be a valid 32-hex UUID")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE dsp_notice_quarantine SET status = 'resolved' WHERE tenant_id = ? AND quarantine_id = ?`,
		s.tenantID, qBytes)
	if err != nil {
		return fmt.Errorf("resolve quarantine: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) GetStatus(ctx context.Context, loadedSnapshot string) (ConfigStatus, error) {
	var manifestVersion uint64
	_ = s.db.QueryRowContext(ctx, `SELECT manifest_version FROM dsp_config_manifest_clock WHERE tenant_id = ?`,
		s.tenantID).Scan(&manifestVersion)

	var pendingCount int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dsp_config_outbox WHERE tenant_id = ? AND published_at IS NULL`,
		s.tenantID).Scan(&pendingCount)

	return ConfigStatus{
		TenantID:           hex.EncodeToString(s.tenantID),
		ManifestVersion:    manifestVersion,
		OutboxPendingCount: pendingCount,
		LoadedSnapshot:     loadedSnapshot,
	}, nil
}
