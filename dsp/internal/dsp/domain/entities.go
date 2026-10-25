package domain

import (
	"fmt"
	"strings"
)

// Campaign represents a high‑level advertising campaign configuration.
// Only a minimal set of fields is defined for the current iteration.
type Campaign struct {
	ID          string   // unique identifier, e.g. UUID
	Name        string   // human‑readable name
	LineItemIDs []string // identifiers of associated line items
	Active      bool
	// Additional fields such as budget, targeting, etc. will be added later.
}

// LineItem represents a specific ad placement configuration under a campaign.
type LineItem struct {
	ID           string   // unique identifier
	CampaignID   string   // parent campaign identifier
	CreativeIDs  []string // creatives that can be served
	Active       bool
	SiteDomain   string
	SitePage     string
	BannerWidth  int
	BannerHeight int
	// Future fields: price, schedule, targeting filters, etc.
}

// Creative holds static creative asset metadata.
type Creative struct {
	ID        string // unique identifier
	AssetMeta AssetMeta
	Active    bool
	// Future fields: markup, click URL, etc.
}

// AssetMeta describes static asset properties used for whitelist validation.
type AssetMeta struct {
	MIMEType string // e.g. "image/png"
	SizeKB   int    // size in kilobytes
	Hash     string // content hash for integrity checking
	Host     string // allowed landing host for click‑through URLs
}

// WhiteListEntry defines a simple whitelist rule for asset delivery.
type WhiteListEntry struct {
	Host string // allowed host pattern
	MIME string // allowed MIME type pattern
	// In a full implementation, this could include regexes or more complex rules.
}

// ValidateVersion ensures the provided version string is non‑empty and follows a simple pattern.
func ValidateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("version string cannot be empty")
	}
	// Basic sanity check: must contain at least one dot (e.g. "1.0").
	if !strings.Contains(v, ".") {
		return fmt.Errorf("invalid version format: %s", v)
	}
	return nil
}
