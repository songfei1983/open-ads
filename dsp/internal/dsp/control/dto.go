// Package control defines the DSP management API DTOs and validation rules.
package control

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/assets"
	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

// ErrInvalidConfiguration indicates a request that cannot be saved or published.
var ErrInvalidConfiguration = errors.New("invalid configuration")

// ErrUnsupportedTargeting indicates targeting outside the supported MVP contract.
var ErrUnsupportedTargeting = errors.New("unsupported targeting")

// ErrVersionConflict indicates that the resource changed after the caller read it.
var ErrVersionConflict = errors.New("version conflict")

// Advertiser is the management representation of an advertiser draft.
type Advertiser struct {
	ID              string `json:"id,omitempty"`
	Status          string `json:"status"`
	DefaultCurrency string `json:"default_currency"`
	Version         uint64 `json:"version"`
}

// Campaign is the management representation of a campaign draft.
type Campaign struct {
	ID            string     `json:"id,omitempty"`
	AdvertiserID  string     `json:"advertiser_id"`
	Status        string     `json:"status"`
	StartsAt      time.Time  `json:"starts_at"`
	EndsAt        *time.Time `json:"ends_at,omitempty"`
	Version       uint64     `json:"version"`
	ConfigVersion string     `json:"config_version,omitempty"`
	PublishStatus string     `json:"publish_status,omitempty"`
}

// LineItem is the management representation of a line item draft.
type LineItem struct {
	ID                   string     `json:"id,omitempty"`
	CampaignID           string     `json:"campaign_id"`
	Status               string     `json:"status"`
	BidCPMMicros         int64      `json:"bid_cpm_micros"`
	Currency             string     `json:"currency"`
	DailyBudgetMicros    int64      `json:"daily_budget_micros"`
	LifetimeBudgetMicros int64      `json:"lifetime_budget_micros"`
	StartsAt             *time.Time `json:"starts_at,omitempty"`
	EndsAt               *time.Time `json:"ends_at,omitempty"`
	Targeting            Targeting  `json:"targeting"`
	CreativeIDs          []string   `json:"creative_ids"`
	Version              uint64     `json:"version"`
	ConfigVersion        string     `json:"config_version,omitempty"`
	PublishStatus        string     `json:"publish_status,omitempty"`
}

// Targeting contains only the contextual targeting dimensions supported in MVP.
type Targeting struct {
	SiteDomains      []string     `json:"site_domains,omitempty"`
	PagePathPrefixes []string     `json:"page_path_prefixes,omitempty"`
	BannerSizes      []BannerSize `json:"banner_sizes,omitempty"`
}

// UnmarshalJSON rejects targeting dimensions outside the MVP contract.
func (t *Targeting) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var decoded Targeting
	for name, value := range fields {
		switch name {
		case "site_domains":
			if err := json.Unmarshal(value, &decoded.SiteDomains); err != nil {
				return err
			}
		case "page_path_prefixes":
			if err := json.Unmarshal(value, &decoded.PagePathPrefixes); err != nil {
				return err
			}
		case "banner_sizes":
			if err := json.Unmarshal(value, &decoded.BannerSizes); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s", ErrUnsupportedTargeting, name)
		}
	}
	*t = decoded
	return nil
}

// BannerSize identifies a supported banner width and height.
type BannerSize struct {
	Width  int `json:"w"`
	Height int `json:"h"`
}

// Creative is the management representation of a creative draft.
type Creative struct {
	ID            string `json:"id,omitempty"`
	Status        string `json:"status"`
	Format        string `json:"format"`
	AssetRef      string `json:"asset_ref"`
	AssetSHA256   string `json:"asset_sha256"`
	Width         int    `json:"width_px"`
	Height        int    `json:"height_px"`
	LandingURL    string `json:"landing_url"`
	Version       uint64 `json:"version"`
	ConfigVersion string `json:"config_version,omitempty"`
	PublishStatus string `json:"publish_status,omitempty"`
}

// CreativeInput accepts only administrator-controlled creative fields; asset
// metadata is always copied from the server-side upload registry.
type CreativeInput struct {
	ID         string `json:"id,omitempty"`
	Status     string `json:"status"`
	AssetRef   string `json:"asset_ref"`
	LandingURL string `json:"landing_url"`
	Version    uint64 `json:"version"`
}

// Validate checks advertiser fields before persistence.
func (a Advertiser) Validate() error {
	if err := validateStatus(a.Status); err != nil {
		return err
	}
	if !validCurrency(a.DefaultCurrency) {
		return invalid("default_currency", "must be a three-letter uppercase currency code")
	}
	return nil
}

func validCurrency(value string) bool {
	if len(value) != 3 || value != strings.ToUpper(value) {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

// Validate checks campaign fields before persistence.
func (c Campaign) Validate() error {
	if err := validateID("advertiser_id", c.AdvertiserID); err != nil {
		return err
	}
	if err := validateStatus(c.Status); err != nil {
		return err
	}
	if c.StartsAt.IsZero() {
		return invalid("starts_at", "is required")
	}
	if c.EndsAt != nil && !c.EndsAt.After(c.StartsAt) {
		return invalid("ends_at", "must be later than starts_at")
	}
	return nil
}

// Validate checks line item fields before persistence.
func (li LineItem) Validate() error {
	if err := validateID("campaign_id", li.CampaignID); err != nil {
		return err
	}
	if err := validateStatus(li.Status); err != nil {
		return err
	}
	if li.BidCPMMicros <= 0 {
		return invalid("bid_cpm_micros", "must be positive")
	}
	if li.DailyBudgetMicros <= 0 || li.LifetimeBudgetMicros <= 0 {
		return invalid("budget", "daily and lifetime budgets must be positive")
	}
	if !validCurrency(li.Currency) {
		return invalid("currency", "must be a three-letter uppercase currency code")
	}
	if li.StartsAt != nil && li.EndsAt != nil && !li.EndsAt.After(*li.StartsAt) {
		return invalid("ends_at", "must be later than starts_at")
	}
	if len(li.CreativeIDs) == 0 {
		return invalid("creative_ids", "at least one creative is required")
	}
	seen := make(map[string]struct{}, len(li.CreativeIDs))
	for _, id := range li.CreativeIDs {
		if err := validateID("creative_ids", id); err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return invalid("creative_ids", "must not contain duplicates")
		}
		seen[id] = struct{}{}
	}
	return li.Targeting.Validate()
}

// Validate checks the supported MVP targeting contract.
func (t Targeting) Validate() error {
	for _, value := range t.SiteDomains {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
		if host == "" || strings.ContainsAny(host, "/?# \t\r\n") {
			return invalid("targeting.site_domains", "must contain normalized host names")
		}
		if strings.Contains(host, ":") || strings.Contains(host, "@") {
			return invalid("targeting.site_domains", "must not contain a port or user information")
		}
	}
	for _, prefix := range t.PagePathPrefixes {
		if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "?#\r\n") {
			return invalid("targeting.page_path_prefixes", "must be absolute paths without query or fragment")
		}
	}
	for _, size := range t.BannerSizes {
		if size.Width < 1 || size.Height < 1 || size.Width > 10000 || size.Height > 10000 {
			return invalid("targeting.banner_sizes", "width and height must be between 1 and 10000")
		}
	}
	return nil
}

// Validate checks creative metadata before it is accepted as a draft.
func (c Creative) Validate() error {
	if err := validateStatus(c.Status); err != nil {
		return err
	}
	if c.Format != "image/png" && c.Format != "image/jpeg" && c.Format != "image/webp" {
		return invalid("format", "only PNG, JPEG, and WebP are supported")
	}
	if strings.TrimSpace(c.AssetRef) == "" {
		return invalid("asset_ref", "is required")
	}
	digest, err := hex.DecodeString(c.AssetSHA256)
	if len(c.AssetSHA256) != 64 || err != nil || strings.ToLower(c.AssetSHA256) != c.AssetSHA256 || len(digest) != 32 {
		return invalid("asset_sha256", "must be a 64-character lowercase SHA-256 hex digest")
	}
	if c.Width < 1 || c.Height < 1 || c.Width > 10000 || c.Height > 10000 {
		return invalid("dimensions", "width and height must be between 1 and 10000")
	}
	landingURL, err := url.ParseRequestURI(c.LandingURL)
	if err != nil || landingURL.Scheme != "https" || landingURL.Host == "" || landingURL.User != nil {
		return invalid("landing_url", "must be an absolute HTTPS URL without user information")
	}
	return nil
}

// Validate checks user-editable creative fields without accepting file metadata.
func (c CreativeInput) Validate() error {
	if err := validateStatus(c.Status); err != nil {
		return err
	}
	if _, _, err := assets.ParseAssetRef(c.AssetRef); err != nil {
		return invalid("asset_ref", "must be a canonical uploaded asset reference")
	}
	landingURL, err := url.ParseRequestURI(c.LandingURL)
	if err != nil || landingURL.Scheme != "https" || landingURL.Host == "" || landingURL.User != nil {
		return invalid("landing_url", "must be an absolute HTTPS URL without user information")
	}
	return nil
}

func validateStatus(status string) error {
	if status != "active" && status != "paused" {
		return invalid("status", "must be active or paused")
	}
	return nil
}

func validateID(field, value string) error {
	if err := domain.ValidateUUID32(value); err != nil {
		return invalid(field, "must be a 32-character lowercase UUID")
	}
	return nil
}

func invalid(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidConfiguration, field, reason)
}

// BudgetSummary represents a summarized budget account record.
type BudgetSummary struct {
	LineItemID        string `json:"line_item_id"`
	BudgetScope       string `json:"budget_scope"`
	PeriodStart       string `json:"period_start"`
	Currency          string `json:"currency"`
	BudgetLimitMicros int64  `json:"budget_limit_micros"`
	SpentMicros       int64  `json:"spent_micros"`
	LeasedMicros      int64  `json:"leased_micros"`
	AvailableMicros   int64  `json:"available_micros"`
	Status            string `json:"status"`
}

// QuarantineRecord represents an isolated notice record.
type QuarantineRecord struct {
	QuarantineID   string `json:"quarantine_id"`
	ReservationID  string `json:"reservation_id"`
	SourceEventKey string `json:"source_event_key"`
	Outcome        string `json:"outcome"`
	ReasonCode     string `json:"reason_code"`
	Status         string `json:"status"`
	ReceivedAt     string `json:"received_at"`
}

// ConfigStatus represents the current publication status and manifest version.
type ConfigStatus struct {
	TenantID           string `json:"tenant_id"`
	ManifestVersion    uint64 `json:"manifest_version"`
	OutboxPendingCount int    `json:"outbox_pending_count"`
	LoadedSnapshot     string `json:"loaded_snapshot_version"`
}
