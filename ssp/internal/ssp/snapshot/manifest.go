// Package snapshot owns the immutable, versioned SSP configuration snapshot.
// The RTB hot path only reads the active snapshot and never queries MariaDB or
// Valkey for configuration.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Supported resource statuses. A draft is never published; a paused resource
// stays in the manifest so operators can resume it without a republish.
const (
	StatusDraft  = "draft"
	StatusActive = "active"
	StatusPaused = "paused"
)

// Auction policy values supported by the sandbox profile.
const (
	TieBreakerPriorityThenHash = "dsp_priority_then_ssp_hash"
	FloorEnforcementStrict     = "strict"
	CurrencyPolicyExactMatch   = "exact_match"
)

var assetRefPattern = regexp.MustCompile(`^as:[0-9a-f]{32}:[0-9a-f]{64}$`)

// Publisher is the tenant-scoped supply owner.
type Publisher struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version uint64 `json:"version"`
}

// Property is a site or app owned by a publisher.
type Property struct {
	ID          string `json:"id"`
	PublisherID string `json:"publisher_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	ExternalID  string `json:"external_id"`
	Status      string `json:"status"`
	Version     uint64 `json:"version"`
}

// AuctionPolicy is the first-price auction policy compiled into a placement.
type AuctionPolicy struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	At               int    `json:"at"`
	TieBreaker       string `json:"tie_breaker"`
	FloorEnforcement string `json:"floor_enforcement"`
	CurrencyPolicy   string `json:"currency_policy"`
	Status           string `json:"status"`
	Version          uint64 `json:"version"`
}

// Placement is the sellable ad slot and its authoritative floor.
type Placement struct {
	ID              string   `json:"id"`
	PropertyID      string   `json:"property_id"`
	AuctionPolicyID string   `json:"auction_policy_id"`
	Name            string   `json:"name"`
	MediaType       string   `json:"media_type"`
	Width           int      `json:"width"`
	Height          int      `json:"height"`
	FloorCPMMicros  int64    `json:"floor_cpm_micros"`
	Currency        string   `json:"currency"`
	PrivacyPolicy   string   `json:"privacy_policy"`
	AllowedOrigins  []string `json:"allowed_origins,omitempty"`
	Status          string   `json:"status"`
	Version         uint64   `json:"version"`
}

// SupplyEndpoint binds an authenticated Supply profile to a property.
type SupplyEndpoint struct {
	ID                 string   `json:"id"`
	PropertyID         string   `json:"property_id"`
	Name               string   `json:"name"`
	ProtocolVersion    string   `json:"protocol_version"`
	CredentialSHA256   string   `json:"credential_sha256,omitempty"`
	AllowedOrigins     []string `json:"allowed_origins,omitempty"`
	RateLimitPerSecond int      `json:"rate_limit_per_second"`
	Status             string   `json:"status"`
	Version            uint64   `json:"version"`
}

// DSPEndpoint is one bidder plus its SSP-side creative whitelist.
type DSPEndpoint struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	BidURL            string              `json:"bid_url"`
	OpenRTBProfile    string              `json:"openrtb_profile"`
	CallbackHosts     []string            `json:"callback_hosts"`
	Priority          int                 `json:"priority"`
	TimeoutMS         int                 `json:"timeout_ms"`
	Status            string              `json:"status"`
	Version           uint64              `json:"version"`
	CreativeWhitelist map[string]Creative `json:"creative_whitelist"`
}

// Creative is an immutable, whitelisted static image registered by the DSP.
type Creative struct {
	ID             string `json:"id"`
	AssetRef       string `json:"asset_ref"`
	AssetSHA256    string `json:"asset_sha256"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	MIMEType       string `json:"mime_type"`
	LandingURLHost string `json:"landing_url_host"`
	LandingURL     string `json:"landing_url"`
	Status         string `json:"status"`
	Version        uint64 `json:"version"`
}

// Manifest is the complete tenant configuration projection.
type Manifest struct {
	Version         string                    `json:"version"`
	Publishers      map[string]Publisher      `json:"publishers"`
	Properties      map[string]Property       `json:"properties"`
	AuctionPolicies map[string]AuctionPolicy  `json:"auction_policies"`
	Placements      map[string]Placement      `json:"placements"`
	SupplyEndpoints map[string]SupplyEndpoint `json:"supply_endpoints"`
	DSPEndpoints    map[string]DSPEndpoint    `json:"dsp_endpoints"`
}

// Validate rejects a manifest the RTB path must never serve.
func Validate(manifest *Manifest) error {
	if manifest == nil {
		return errors.New("nil SSP manifest")
	}
	if err := ValidateVersion(manifest.Version); err != nil {
		return err
	}
	for id, publisher := range manifest.Publishers {
		if id != publisher.ID || ValidateID(id) != nil {
			return fmt.Errorf("publisher id mismatch %q", id)
		}
		if _, ok := resourceStatus(publisher.Status); !ok {
			return fmt.Errorf("publisher %q has invalid status %q", id, publisher.Status)
		}
	}
	for id, property := range manifest.Properties {
		if id != property.ID {
			return fmt.Errorf("property id mismatch %q", id)
		}
		if _, ok := manifest.Publishers[property.PublisherID]; !ok {
			return fmt.Errorf("property %q has no publisher", id)
		}
		if _, ok := resourceStatus(property.Status); !ok {
			return fmt.Errorf("property %q has invalid status %q", id, property.Status)
		}
	}
	for id, policy := range manifest.AuctionPolicies {
		if id != policy.ID {
			return fmt.Errorf("auction policy id mismatch %q", id)
		}
		if policy.At != 1 {
			return fmt.Errorf("auction policy %q uses unsupported at=%d", id, policy.At)
		}
		if policy.TieBreaker != TieBreakerPriorityThenHash {
			return fmt.Errorf("auction policy %q uses unsupported tie breaker %q", id, policy.TieBreaker)
		}
		if policy.FloorEnforcement != FloorEnforcementStrict {
			return fmt.Errorf("auction policy %q uses unsupported floor enforcement %q", id, policy.FloorEnforcement)
		}
		if policy.CurrencyPolicy != CurrencyPolicyExactMatch {
			return fmt.Errorf("auction policy %q uses unsupported currency policy %q", id, policy.CurrencyPolicy)
		}
		if _, ok := resourceStatus(policy.Status); !ok {
			return fmt.Errorf("auction policy %q has invalid status %q", id, policy.Status)
		}
	}
	for id, placement := range manifest.Placements {
		if id != placement.ID {
			return fmt.Errorf("placement id mismatch %q", id)
		}
		if _, ok := manifest.Properties[placement.PropertyID]; !ok {
			return fmt.Errorf("placement %q has no property", id)
		}
		if _, ok := manifest.AuctionPolicies[placement.AuctionPolicyID]; !ok {
			return fmt.Errorf("placement %q has no auction policy", id)
		}
		if placement.Width <= 0 || placement.Height <= 0 || placement.Width > 4096 || placement.Height > 4096 {
			return fmt.Errorf("placement %q has invalid size %dx%d", id, placement.Width, placement.Height)
		}
		if placement.FloorCPMMicros < 0 {
			return fmt.Errorf("placement %q has a negative floor", id)
		}
		if len(placement.Currency) != 3 || strings.ToUpper(placement.Currency) != placement.Currency {
			return fmt.Errorf("placement %q has invalid currency %q", id, placement.Currency)
		}
		if _, ok := resourceStatus(placement.Status); !ok {
			return fmt.Errorf("placement %q has invalid status %q", id, placement.Status)
		}
	}
	for id, endpoint := range manifest.SupplyEndpoints {
		if id != endpoint.ID {
			return fmt.Errorf("supply endpoint id mismatch %q", id)
		}
		if _, ok := manifest.Properties[endpoint.PropertyID]; !ok {
			return fmt.Errorf("supply endpoint %q has no property", id)
		}
		if endpoint.Status == StatusActive {
			if len(endpoint.CredentialSHA256) != 64 || !isLowerHex(endpoint.CredentialSHA256) {
				return fmt.Errorf("supply endpoint %q has no valid credential hash", id)
			}
		}
		if endpoint.RateLimitPerSecond < 0 {
			return fmt.Errorf("supply endpoint %q has a negative rate limit", id)
		}
	}
	for id, endpoint := range manifest.DSPEndpoints {
		if id != endpoint.ID {
			return fmt.Errorf("dsp endpoint id mismatch %q", id)
		}
		parsed, err := url.Parse(endpoint.BidURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("dsp endpoint %q has an invalid bid URL", id)
		}
		if endpoint.TimeoutMS <= 0 || endpoint.TimeoutMS > 5000 {
			return fmt.Errorf("dsp endpoint %q has an invalid timeout", id)
		}
		if _, ok := resourceStatus(endpoint.Status); !ok {
			return fmt.Errorf("dsp endpoint %q has invalid status %q", id, endpoint.Status)
		}
		if endpoint.Status == StatusActive && len(endpoint.CallbackHosts) == 0 {
			return fmt.Errorf("dsp endpoint %q has no callback host allowlist", id)
		}
		for creativeID, creative := range endpoint.CreativeWhitelist {
			if creativeID != creative.ID {
				return fmt.Errorf("creative id mismatch %q", creativeID)
			}
			if !assetRefPattern.MatchString(creative.AssetRef) {
				return fmt.Errorf("creative %q has an invalid asset ref", creativeID)
			}
			if len(creative.AssetSHA256) != 64 || !isLowerHex(creative.AssetSHA256) {
				return fmt.Errorf("creative %q has an invalid asset hash", creativeID)
			}
			if creative.Width <= 0 || creative.Height <= 0 {
				return fmt.Errorf("creative %q has invalid dimensions", creativeID)
			}
			if _, ok := SupportedMIME(creative.MIMEType); !ok {
				return fmt.Errorf("creative %q has unsupported mime %q", creativeID, creative.MIMEType)
			}
			if creative.LandingURLHost == "" {
				return fmt.Errorf("creative %q has no landing host", creativeID)
			}
			if _, ok := resourceStatus(creative.Status); !ok {
				return fmt.Errorf("creative %q has invalid status %q", creativeID, creative.Status)
			}
		}
	}
	return nil
}

// AssetRefValid reports whether a DSP asset reference is safe to map to the
// content-addressed store.
func AssetRefValid(value string) bool { return assetRefPattern.MatchString(value) }

// ValidateID verifies the wire identifier form used across the SSP.
func ValidateID(id string) error {
	if len(id) != 32 {
		return fmt.Errorf("invalid id length %d", len(id))
	}
	if !isLowerHex(id) {
		return fmt.Errorf("id must be lowercase hex")
	}
	return nil
}

// ValidateVersion verifies the ordered twenty digit manifest version.
func ValidateVersion(version string) error {
	if len(version) != 20 || !isDigits(version) {
		return fmt.Errorf("invalid manifest version %q", version)
	}
	return nil
}

// FormatVersion renders a comparable twenty digit manifest version.
func FormatVersion(value uint64) string { return fmt.Sprintf("%020d", value) }

// Supports reports whether the manifest carries the active placement and all
// of its referenced resources.
func (m *Manifest) Supports(placementID string) (Placement, bool) {
	placement, ok := m.Placements[placementID]
	if !ok || placement.Status != StatusActive {
		return Placement{}, false
	}
	property, ok := m.Properties[placement.PropertyID]
	if !ok || property.Status != StatusActive {
		return Placement{}, false
	}
	publisher, ok := m.Publishers[property.PublisherID]
	if !ok || publisher.Status != StatusActive {
		return Placement{}, false
	}
	policy, ok := m.AuctionPolicies[placement.AuctionPolicyID]
	if !ok || policy.Status != StatusActive {
		return Placement{}, false
	}
	return placement, true
}

// SupplyByCredentialHash resolves an authenticated Supply credential to its
// published endpoint. Credentials are compared as digests only.
func (m *Manifest) SupplyByCredentialHash(hash string) (SupplyEndpoint, bool) {
	for _, endpoint := range m.SupplyEndpoints {
		if endpoint.Status != StatusActive || endpoint.CredentialSHA256 == "" {
			continue
		}
		if secureEqual(endpoint.CredentialSHA256, hash) {
			if _, ok := m.Properties[endpoint.PropertyID]; !ok {
				return SupplyEndpoint{}, false
			}
			return endpoint, true
		}
	}
	return SupplyEndpoint{}, false
}

// ActiveDSPEndpoints returns the bidders eligible for an auction ordered by
// configured priority and stable id.
func (m *Manifest) ActiveDSPEndpoints() []DSPEndpoint {
	endpoints := make([]DSPEndpoint, 0, len(m.DSPEndpoints))
	for _, endpoint := range m.DSPEndpoints {
		if endpoint.Status == StatusActive {
			endpoints = append(endpoints, endpoint)
		}
	}
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].Priority != endpoints[j].Priority {
			return endpoints[i].Priority < endpoints[j].Priority
		}
		return endpoints[i].ID < endpoints[j].ID
	})
	return endpoints
}

// ActiveCreative returns a whitelisted creative that the DSP may bid with.
func (e DSPEndpoint) ActiveCreative(creativeID string) (Creative, bool) {
	creative, ok := e.CreativeWhitelist[creativeID]
	if !ok || creative.Status != StatusActive {
		return Creative{}, false
	}
	return creative, true
}

// CallbackAllowed reports whether a bid notice URL may be contacted.
func (e DSPEndpoint) CallbackAllowed(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	if scheme := strings.ToLower(parsed.Scheme); scheme != "https" && scheme != "http" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range e.CallbackHosts {
		if strings.ToLower(strings.TrimSuffix(strings.TrimSpace(allowed), ".")) == host {
			return true
		}
	}
	return false
}

// SupportedMIME reports whether the renderer may serve the creative MIME type.
func SupportedMIME(mime string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return "image/png", true
	case "image/jpeg":
		return "image/jpeg", true
	case "image/webp":
		return "image/webp", true
	default:
		return "", false
	}
}

// NormalizeOrigin lowercases and validates a scheme://host[:port] origin.
func NormalizeOrigin(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", false
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), true
}

// OriginAllowed applies an exact origin allowlist. An empty allowlist keeps the
// sandbox open to non-browser callers; configured entries are always exact.
func OriginAllowed(allowed []string, origin string) bool {
	if len(allowed) == 0 {
		return true
	}
	normalized, ok := NormalizeOrigin(origin)
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if normalized == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

// CanonicalOrigins normalizes and de-duplicates an operator supplied allowlist.
func CanonicalOrigins(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		normalized, ok := NormalizeOrigin(value)
		if !ok {
			return nil, fmt.Errorf("invalid origin %q", value)
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

// HashSHA256Hex returns the lowercase hex digest of a secret.
func HashSHA256Hex(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func isLowerHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func resourceStatus(value string) (string, bool) {
	switch value {
	case StatusDraft, StatusActive, StatusPaused:
		return value, true
	default:
		return "", false
	}
}
