package snapshot_test

import (
	"strings"
	"testing"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

const (
	tenantID    = "018f7a3b2c9d7e1fa3b4c5d6e7f89001"
	publisherID = "018f7a3b2c9d7e1fa3b4c5d6e7f89002"
	propertyID  = "018f7a3b2c9d7e1fa3b4c5d6e7f89003"
	policyID    = "018f7a3b2c9d7e1fa3b4c5d6e7f89004"
	placementID = "018f7a3b2c9d7e1fa3b4c5d6e7f89005"
	supplyID    = "018f7a3b2c9d7e1fa3b4c5d6e7f89006"
	dspID       = "018f7a3b2c9d7e1fa3b4c5d6e7f89007"
	creativeID  = "018f7a3b2c9d7e1fa3b4c5d6e7f89008"
	assetSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	assetRef    = "as:" + tenantID + ":" + assetSHA256
)

func validManifest() *snapshot.Manifest {
	return &snapshot.Manifest{
		Version: snapshot.FormatVersion(12),
		Publishers: map[string]snapshot.Publisher{
			publisherID: {ID: publisherID, Name: "Acme", Status: snapshot.StatusActive, Version: 3},
		},
		Properties: map[string]snapshot.Property{
			propertyID: {ID: propertyID, PublisherID: publisherID, Name: "Site", Type: "site", ExternalID: "publisher.test", Status: snapshot.StatusActive, Version: 2},
		},
		AuctionPolicies: map[string]snapshot.AuctionPolicy{
			policyID: {ID: policyID, Name: "First price", At: 1, TieBreaker: snapshot.TieBreakerPriorityThenHash,
				FloorEnforcement: snapshot.FloorEnforcementStrict, CurrencyPolicy: snapshot.CurrencyPolicyExactMatch,
				Status: snapshot.StatusActive, Version: 1},
		},
		Placements: map[string]snapshot.Placement{
			placementID: {ID: placementID, PropertyID: propertyID, AuctionPolicyID: policyID, Name: "Leaderboard",
				MediaType: "banner", Width: 728, Height: 90, FloorCPMMicros: 1_200_000, Currency: "USD",
				PrivacyPolicy: "consent_required", AllowedOrigins: []string{"https://publisher.test"}, Status: snapshot.StatusActive, Version: 4},
		},
		SupplyEndpoints: map[string]snapshot.SupplyEndpoint{
			supplyID: {ID: supplyID, PropertyID: propertyID, Name: "Supply", ProtocolVersion: "openrtb2.6",
				CredentialSHA256: snapshot.HashSHA256Hex("supply-secret-value"), AllowedOrigins: []string{"https://publisher.test"},
				RateLimitPerSecond: 100, Status: snapshot.StatusActive, Version: 1},
		},
		DSPEndpoints: map[string]snapshot.DSPEndpoint{
			dspID: {ID: dspID, Name: "DSP", BidURL: "https://dsp.test/openrtb2/bid", OpenRTBProfile: "openrtb2.6",
				CallbackHosts: []string{"dsp.test"}, Priority: 10, TimeoutMS: 50, Status: snapshot.StatusActive, Version: 5,
				CreativeWhitelist: map[string]snapshot.Creative{
					creativeID: {ID: creativeID, AssetRef: assetRef, AssetSHA256: assetSHA256, Width: 728, Height: 90,
						MIMEType: "image/png", LandingURLHost: "publisher.test", LandingURL: "https://publisher.test/landing",
						Status: snapshot.StatusActive, Version: 2},
				}},
		},
	}
}

func TestValidateAcceptsCompleteManifest(t *testing.T) {
	if err := snapshot.Validate(validManifest()); err != nil {
		t.Fatalf("expected valid manifest, got %v", err)
	}
}

func TestValidateRejectsUnsupportedPolicy(t *testing.T) {
	manifest := validManifest()
	policy := manifest.AuctionPolicies[policyID]
	policy.At = 2
	manifest.AuctionPolicies[policyID] = policy
	if err := snapshot.Validate(manifest); err == nil || !strings.Contains(err.Error(), "at=2") {
		t.Fatalf("expected at=2 rejection, got %v", err)
	}
}

func TestValidateRejectsUnsafeAssetRef(t *testing.T) {
	manifest := validManifest()
	endpoint := manifest.DSPEndpoints[dspID]
	creative := endpoint.CreativeWhitelist[creativeID]
	creative.AssetRef = "as:../../etc/passwd"
	endpoint.CreativeWhitelist[creativeID] = creative
	manifest.DSPEndpoints[dspID] = endpoint
	if err := snapshot.Validate(manifest); err == nil {
		t.Fatal("expected invalid asset ref to be rejected")
	}
}

func TestSupportsRequiresActiveParents(t *testing.T) {
	manifest := validManifest()
	placement, ok := manifest.Supports(placementID)
	if !ok || placement.ID != placementID {
		t.Fatal("expected active placement to be supported")
	}
	property := manifest.Properties[propertyID]
	property.Status = snapshot.StatusPaused
	manifest.Properties[propertyID] = property
	if _, ok := manifest.Supports(placementID); ok {
		t.Fatal("paused property must not serve placements")
	}
}

func TestSupplyCredentialLookup(t *testing.T) {
	manifest := validManifest()
	endpoint, ok := manifest.SupplyByCredentialHash(snapshot.HashSHA256Hex("supply-secret-value"))
	if !ok || endpoint.ID != supplyID {
		t.Fatal("expected supply credential to resolve")
	}
	if _, ok := manifest.SupplyByCredentialHash(snapshot.HashSHA256Hex("wrong")); ok {
		t.Fatal("wrong credential must not resolve")
	}
}

func TestOriginPolicy(t *testing.T) {
	allowed := []string{"https://publisher.test"}
	if !snapshot.OriginAllowed(allowed, "https://PUBLISHER.test") {
		t.Fatal("expected case-insensitive exact origin match")
	}
	if snapshot.OriginAllowed(allowed, "https://evil.test") {
		t.Fatal("unexpected cross-origin allowance")
	}
	origins, err := snapshot.CanonicalOrigins([]string{"https://Publisher.test/", "https://publisher.test", " http://localhost:3000 "})
	if err != nil {
		t.Fatal(err)
	}
	if len(origins) != 2 {
		t.Fatalf("expected normalized de-duplicated origins, got %v", origins)
	}
}

func TestActiveDSPEndpointsOrdered(t *testing.T) {
	manifest := validManifest()
	second := manifest.DSPEndpoints[dspID]
	second.ID = "018f7a3b2c9d7e1fa3b4c5d6e7f89009"
	second.Priority = 5
	manifest.DSPEndpoints[second.ID] = second
	endpoints := manifest.ActiveDSPEndpoints()
	if len(endpoints) != 2 || endpoints[0].ID != second.ID {
		t.Fatalf("expected priority order, got %v", endpoints)
	}
}

func TestCallbackAllowedRejectsUnsafeTargets(t *testing.T) {
	endpoint := snapshot.DSPEndpoint{
		CallbackHosts: []string{"dsp.test", "127.0.0.1", "internal.dsp.test"},
	}
	allowed := []string{
		"https://dsp.test/win", "http://dsp.test/loss", "https://dsp.test:8443/bill?price=1",
	}
	for _, rawURL := range allowed {
		if !endpoint.CallbackAllowed(rawURL) {
			t.Errorf("CallbackAllowed(%q) = false, want true", rawURL)
		}
	}
	blocked := []string{
		"", "ftp://dsp.test/win", "javascript:alert(1)", "file:///etc/passwd",
		"https://evil.test/win", "https://dsp.test.evil.test/win",
	}
	for _, rawURL := range blocked {
		if endpoint.CallbackAllowed(rawURL) {
			t.Errorf("CallbackAllowed(%q) = true, want false", rawURL)
		}
	}
}
