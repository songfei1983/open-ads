package control

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

// API error codes shared by the management and internal interfaces.
const (
	CodeInvalidRequest      = "invalid_request"
	CodeVersionConflict     = "version_conflict"
	CodeNotFound            = "not_found"
	CodeResourceConflict    = "resource_conflict"
	CodeInvalidReference    = "invalid_reference"
	CodeBusinessRule        = "business_rule_violation"
	CodeUnsupportedProfile  = "unsupported_profile"
	CodeUnsupportedCreative = "unsupported_creative"
	CodeInternal            = "internal_error"
)

// Publisher is the operator-facing publisher projection.
type Publisher struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Status           string `json:"status"`
	Version          uint64 `json:"version"`
	PublishedVersion uint64 `json:"published_version"`
}

// PublisherInput is the mutable publisher payload.
type PublisherInput struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version uint64 `json:"version"`
}

// Property is the operator-facing property projection.
type Property struct {
	ID               string `json:"id"`
	PublisherID      string `json:"publisher_id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	ExternalID       string `json:"external_id"`
	Status           string `json:"status"`
	Version          uint64 `json:"version"`
	PublishedVersion uint64 `json:"published_version"`
}

// PropertyInput is the mutable property payload.
type PropertyInput struct {
	PublisherID string `json:"publisher_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	ExternalID  string `json:"external_id"`
	Status      string `json:"status"`
	Version     uint64 `json:"version"`
}

// AuctionPolicy is the operator-facing policy projection.
type AuctionPolicy struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	At               int    `json:"at"`
	TieBreaker       string `json:"tie_breaker"`
	FloorEnforcement string `json:"floor_enforcement"`
	CurrencyPolicy   string `json:"currency_policy"`
	Status           string `json:"status"`
	Version          uint64 `json:"version"`
	PublishedVersion uint64 `json:"published_version"`
}

// AuctionPolicyInput is the mutable policy payload.
type AuctionPolicyInput struct {
	Name             string `json:"name"`
	At               int    `json:"at"`
	TieBreaker       string `json:"tie_breaker"`
	FloorEnforcement string `json:"floor_enforcement"`
	CurrencyPolicy   string `json:"currency_policy"`
	Version          uint64 `json:"version"`
}

// Placement is the operator-facing placement projection.
type Placement struct {
	ID               string   `json:"id"`
	PropertyID       string   `json:"property_id"`
	AuctionPolicyID  string   `json:"auction_policy_id"`
	Name             string   `json:"name"`
	MediaType        string   `json:"media_type"`
	Width            int      `json:"width"`
	Height           int      `json:"height"`
	FloorCPMMicros   int64    `json:"floor_cpm_micros"`
	Currency         string   `json:"currency"`
	PrivacyPolicy    string   `json:"privacy_policy"`
	AllowedOrigins   []string `json:"allowed_origins"`
	Status           string   `json:"status"`
	Version          uint64   `json:"version"`
	PublishedVersion uint64   `json:"published_version"`
}

// PlacementInput is the mutable placement payload.
type PlacementInput struct {
	PropertyID      string   `json:"property_id"`
	AuctionPolicyID string   `json:"auction_policy_id"`
	Name            string   `json:"name"`
	MediaType       string   `json:"media_type"`
	Width           int      `json:"width"`
	Height          int      `json:"height"`
	FloorCPMMicros  int64    `json:"floor_cpm_micros"`
	Currency        string   `json:"currency"`
	PrivacyPolicy   string   `json:"privacy_policy"`
	AllowedOrigins  []string `json:"allowed_origins"`
	Status          string   `json:"status"`
	Version         uint64   `json:"version"`
}

// SupplyEndpoint is the operator-facing Supply profile projection.
type SupplyEndpoint struct {
	ID                   string   `json:"id"`
	PropertyID           string   `json:"property_id"`
	Name                 string   `json:"name"`
	ProtocolVersion      string   `json:"protocol_version"`
	CredentialConfigured bool     `json:"credential_configured"`
	AllowedOrigins       []string `json:"allowed_origins"`
	RateLimitPerSecond   int      `json:"rate_limit_per_second"`
	Status               string   `json:"status"`
	Version              uint64   `json:"version"`
	PublishedVersion     uint64   `json:"published_version"`
}

// SupplyEndpointInput is the mutable Supply profile payload. Credential is
// write-only; an omitted credential keeps the previously stored digest.
type SupplyEndpointInput struct {
	PropertyID         string   `json:"property_id"`
	Name               string   `json:"name"`
	ProtocolVersion    string   `json:"protocol_version"`
	Credential         string   `json:"credential"`
	AllowedOrigins     []string `json:"allowed_origins"`
	RateLimitPerSecond int      `json:"rate_limit_per_second"`
	Status             string   `json:"status"`
	Version            uint64   `json:"version"`
}

// DSPEndpoint is the operator-facing bidder projection.
type DSPEndpoint struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	BidURL           string   `json:"bid_url"`
	OpenRTBProfile   string   `json:"openrtb_profile"`
	CallbackHosts    []string `json:"callback_hosts"`
	Priority         int      `json:"priority"`
	TimeoutMS        int      `json:"timeout_ms"`
	Status           string   `json:"status"`
	Version          uint64   `json:"version"`
	PublishedVersion uint64   `json:"published_version"`
}

// DSPEndpointInput is the mutable bidder payload.
type DSPEndpointInput struct {
	Name           string   `json:"name"`
	BidURL         string   `json:"bid_url"`
	OpenRTBProfile string   `json:"openrtb_profile"`
	CallbackHosts  []string `json:"callback_hosts"`
	Priority       int      `json:"priority"`
	TimeoutMS      int      `json:"timeout_ms"`
	Status         string   `json:"status"`
	Version        uint64   `json:"version"`
}

// CreativeWhitelistEntry is the read-only SSP projection of the DSP registry.
type CreativeWhitelistEntry struct {
	CreativeID     string `json:"creative_id"`
	AssetRef       string `json:"asset_ref"`
	AssetSHA256    string `json:"asset_sha256"`
	Width          int    `json:"width_px"`
	Height         int    `json:"height_px"`
	MIMEType       string `json:"mime_type"`
	LandingURLHost string `json:"landing_url_host"`
	Status         string `json:"status"`
	Version        uint64 `json:"version"`
}

// CreativeWhitelistResponse is the read-only whitelist response.
type CreativeWhitelistResponse struct {
	DSPEndpointID string                   `json:"dsp_endpoint_id"`
	Whitelist     []CreativeWhitelistEntry `json:"whitelist"`
}

// CreativeRegistration is one DSP to SSP registry upsert entry.
type CreativeRegistration struct {
	CreativeID     string `json:"creative_id"`
	AssetRef       string `json:"asset_ref"`
	AssetSHA256    string `json:"asset_sha256"`
	Width          int    `json:"width_px"`
	Height         int    `json:"height_px"`
	MIMEType       string `json:"mime_type"`
	LandingURLHost string `json:"landing_url_host"`
	LandingURL     string `json:"landing_url"`
	Status         string `json:"status"`
	Version        uint64 `json:"version"`
}

// CreativeRegistryUpsert is the internal sync request body.
type CreativeRegistryUpsert struct {
	Creatives []CreativeRegistration `json:"creatives"`
}

// CreativeRegistryRejection explains why one registry entry was refused.
type CreativeRegistryRejection struct {
	CreativeID string `json:"creative_id"`
	Reason     string `json:"reason"`
}

// CreativeRegistryResult is the internal sync response body.
type CreativeRegistryResult struct {
	AcceptedCount int                         `json:"accepted_count"`
	Rejected      []CreativeRegistryRejection `json:"rejected"`
}

// PublishRequest is the optional body of a publish action.
type PublishRequest struct {
	Version uint64 `json:"version"`
	Reason  string `json:"reason"`
}

// OutboxEvent is a pending or failed configuration publish attempt.
type OutboxEvent struct {
	EventID            string `json:"event_id"`
	AggregateType      string `json:"aggregate_type"`
	AggregateID        string `json:"aggregate_id"`
	AggregateVersion   uint64 `json:"aggregate_version"`
	CreatedAt          string `json:"created_at"`
	FailedAttempts     int    `json:"failed_attempts"`
	LastFailedAt       string `json:"last_failed_at,omitempty"`
	LastErrorSanitized string `json:"last_error_sanitized,omitempty"`
}

// RuntimeInstance is one instance heartbeat observed by the control plane.
type RuntimeInstance struct {
	InstanceID      string `json:"instance_id"`
	ManifestVersion string `json:"manifest_version"`
	ObservedVersion string `json:"observed_version"`
	Ready           bool   `json:"ready"`
	HeartbeatUnix   int64  `json:"heartbeat_unix"`
	Stale           bool   `json:"stale"`
}

// ValidatePublisherInput checks names and status transitions.
func ValidatePublisherInput(input *PublisherInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return invalidField("name", "is required and must be at most 128 characters")
	}
	if input.Status == "" {
		input.Status = snapshot.StatusActive
	}
	if err := validateStatus(input.Status); err != nil {
		return err
	}
	if !creating && input.Version == 0 {
		return invalidField("version", "is required for updates")
	}
	return nil
}

// ValidatePropertyInput checks property references and identity fields.
func ValidatePropertyInput(input *PropertyInput, creating bool) error {
	if err := validateID("publisher_id", input.PublisherID); err != nil {
		return err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	if input.Name == "" || len(input.Name) > 128 {
		return invalidField("name", "is required and must be at most 128 characters")
	}
	if input.Type != "site" && input.Type != "app" {
		return invalidField("type", "must be site or app")
	}
	if input.ExternalID == "" || len(input.ExternalID) > 128 {
		return invalidField("external_id", "is required and must be at most 128 characters")
	}
	if input.Status == "" {
		input.Status = snapshot.StatusActive
	}
	if err := validateStatus(input.Status); err != nil {
		return err
	}
	if !creating && input.Version == 0 {
		return invalidField("version", "is required for updates")
	}
	return nil
}

// ValidateAuctionPolicyInput enforces the sandbox first-price profile.
func ValidateAuctionPolicyInput(input *AuctionPolicyInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return invalidField("name", "is required and must be at most 128 characters")
	}
	if input.At == 0 {
		input.At = 1
	}
	if input.At != 1 {
		return &apiError{status: 422, code: CodeUnsupportedProfile, message: "only first-price at=1 is supported"}
	}
	if input.TieBreaker == "" {
		input.TieBreaker = snapshot.TieBreakerPriorityThenHash
	}
	if input.TieBreaker != snapshot.TieBreakerPriorityThenHash {
		return &apiError{status: 422, code: CodeUnsupportedProfile, message: "unsupported tie_breaker"}
	}
	if input.FloorEnforcement == "" {
		input.FloorEnforcement = snapshot.FloorEnforcementStrict
	}
	if input.FloorEnforcement != snapshot.FloorEnforcementStrict {
		return &apiError{status: 422, code: CodeUnsupportedProfile, message: "unsupported floor_enforcement"}
	}
	if input.CurrencyPolicy == "" {
		input.CurrencyPolicy = snapshot.CurrencyPolicyExactMatch
	}
	if input.CurrencyPolicy != snapshot.CurrencyPolicyExactMatch {
		return &apiError{status: 422, code: CodeUnsupportedProfile, message: "unsupported currency_policy"}
	}
	if !creating && input.Version == 0 {
		return invalidField("version", "is required for updates")
	}
	return nil
}

// ValidatePlacementInput checks slot geometry, floor and privacy policy.
func ValidatePlacementInput(input *PlacementInput, creating bool) error {
	if err := validateID("property_id", input.PropertyID); err != nil {
		return err
	}
	if err := validateID("auction_policy_id", input.AuctionPolicyID); err != nil {
		return err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return invalidField("name", "is required and must be at most 128 characters")
	}
	if input.MediaType == "" {
		input.MediaType = "banner"
	}
	if input.MediaType != "banner" {
		return invalidField("media_type", "supports only banner")
	}
	if input.Width <= 0 || input.Height <= 0 || input.Width > 4096 || input.Height > 4096 {
		return invalidField("size", "width and height must be between 1 and 4096")
	}
	if input.FloorCPMMicros < 0 {
		return invalidField("floor_cpm_micros", "must not be negative")
	}
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if len(input.Currency) != 3 {
		return invalidField("currency", "must be a three letter code")
	}
	if input.PrivacyPolicy == "" {
		input.PrivacyPolicy = "consent_required"
	}
	switch input.PrivacyPolicy {
	case "consent_required", "no_identity":
	default:
		return invalidField("privacy_policy", "must be consent_required or no_identity")
	}
	origins, err := snapshot.CanonicalOrigins(input.AllowedOrigins)
	if err != nil {
		return invalidField("allowed_origins", err.Error())
	}
	input.AllowedOrigins = origins
	if input.Status == "" {
		input.Status = snapshot.StatusActive
	}
	if err := validateStatus(input.Status); err != nil {
		return err
	}
	if !creating && input.Version == 0 {
		return invalidField("version", "is required for updates")
	}
	return nil
}

// ValidateSupplyEndpointInput checks the Supply profile and credential.
func ValidateSupplyEndpointInput(input *SupplyEndpointInput, creating bool) error {
	if err := validateID("property_id", input.PropertyID); err != nil {
		return err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return invalidField("name", "is required and must be at most 128 characters")
	}
	if input.ProtocolVersion == "" {
		input.ProtocolVersion = "openrtb2.6"
	}
	if input.ProtocolVersion != "openrtb2.6" {
		return &apiError{status: 422, code: CodeUnsupportedProfile, message: "only openrtb2.6 is supported"}
	}
	if input.Credential != "" && len(input.Credential) < 24 {
		return invalidField("credential", "must be at least 24 characters")
	}
	origins, err := snapshot.CanonicalOrigins(input.AllowedOrigins)
	if err != nil {
		return invalidField("allowed_origins", err.Error())
	}
	input.AllowedOrigins = origins
	if input.RateLimitPerSecond < 0 {
		return invalidField("rate_limit_per_second", "must not be negative")
	}
	if input.Status == "" {
		input.Status = snapshot.StatusActive
	}
	if err := validateStatus(input.Status); err != nil {
		return err
	}
	if !creating && input.Version == 0 {
		return invalidField("version", "is required for updates")
	}
	return nil
}

// ValidateDSPEndpointInput checks the bidder URL, profile and SSRF allowlist.
func ValidateDSPEndpointInput(input *DSPEndpointInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return invalidField("name", "is required and must be at most 128 characters")
	}
	parsed, err := url.Parse(strings.TrimSpace(input.BidURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return invalidField("bid_url", "must be an absolute http(s) URL")
	}
	input.BidURL = parsed.String()
	if input.OpenRTBProfile == "" {
		input.OpenRTBProfile = "openrtb2.6"
	}
	if input.OpenRTBProfile != "openrtb2.6" {
		return &apiError{status: 422, code: CodeUnsupportedProfile, message: "only openrtb2.6 is supported"}
	}
	hosts := make([]string, 0, len(input.CallbackHosts))
	seen := make(map[string]struct{}, len(input.CallbackHosts))
	for _, host := range input.CallbackHosts {
		normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		if normalized == "" || strings.ContainsAny(normalized, "/:") {
			return invalidField("callback_hosts", "must contain bare host names")
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		hosts = append(hosts, normalized)
	}
	if input.Status == "" {
		input.Status = snapshot.StatusActive
	}
	if err := validateStatus(input.Status); err != nil {
		return err
	}
	if input.Status == snapshot.StatusActive && len(hosts) == 0 {
		return invalidField("callback_hosts", "is required for an active endpoint")
	}
	input.CallbackHosts = hosts
	if input.TimeoutMS == 0 {
		input.TimeoutMS = 50
	}
	if input.TimeoutMS < 1 || input.TimeoutMS > 5000 {
		return invalidField("timeout_ms", "must be between 1 and 5000")
	}
	if input.Priority < 0 {
		return invalidField("priority", "must not be negative")
	}
	if !creating && input.Version == 0 {
		return invalidField("version", "is required for updates")
	}
	return nil
}

// ValidateCreativeRegistration validates one DSP registry entry.
func ValidateCreativeRegistration(entry *CreativeRegistration) error {
	if err := validateID("creative_id", entry.CreativeID); err != nil {
		return err
	}
	entry.AssetRef = strings.ToLower(strings.TrimSpace(entry.AssetRef))
	if !snapshot.AssetRefValid(entry.AssetRef) {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "asset_ref is invalid"}
	}
	entry.AssetSHA256 = strings.ToLower(strings.TrimSpace(entry.AssetSHA256))
	if len(entry.AssetSHA256) != 64 {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "asset_sha256 must be 64 hex characters"}
	}
	if entry.Width <= 0 || entry.Height <= 0 || entry.Width > 4096 || entry.Height > 4096 {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "creative dimensions are invalid"}
	}
	mime, ok := snapshot.SupportedMIME(entry.MIMEType)
	if !ok {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "mime_type is not supported"}
	}
	entry.MIMEType = mime
	landing, err := url.Parse(strings.TrimSpace(entry.LandingURL))
	if err != nil || landing.Host == "" || (landing.Scheme != "http" && landing.Scheme != "https") {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "landing_url must be an absolute http(s) URL"}
	}
	host := strings.ToLower(strings.TrimSuffix(landing.Hostname(), "."))
	declared := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry.LandingURLHost), "."))
	if declared != "" && declared != host {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "landing_url_host does not match landing_url"}
	}
	entry.LandingURLHost = host
	entry.LandingURL = landing.String()
	if entry.Status == "" {
		entry.Status = snapshot.StatusActive
	}
	if entry.Status != snapshot.StatusActive && entry.Status != snapshot.StatusPaused {
		return &apiError{status: 422, code: CodeUnsupportedCreative, message: "status must be active or paused"}
	}
	return nil
}

func validateStatus(status string) error {
	switch status {
	case snapshot.StatusActive, snapshot.StatusPaused, snapshot.StatusDraft:
		return nil
	default:
		return invalidField("status", "must be draft, active or paused")
	}
}

func validateID(field, value string) error {
	if err := snapshot.ValidateID(strings.TrimSpace(value)); err != nil {
		return invalidField(field, "must be a 32 character lowercase hex id")
	}
	return nil
}

// apiError carries a stable HTTP contract for handler mapping.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: %s", e.code, e.message) }

func invalidField(field, message string) error {
	return &apiError{status: 422, code: CodeBusinessRule, message: field + " " + message}
}
