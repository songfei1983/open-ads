package control

import (
	"strings"
	"testing"
)

func validPlacementInput() PlacementInput {
	return PlacementInput{PropertyID: strings.Repeat("a", 32), AuctionPolicyID: strings.Repeat("b", 32),
		Name: "Leaderboard", MediaType: "banner", Width: 728, Height: 90, FloorCPMMicros: 1_200_000,
		Currency: "usd", PrivacyPolicy: "consent_required", AllowedOrigins: []string{"https://Publisher.test/"},
		Status: "active"}
}

func TestValidatePlacementInputNormalizes(t *testing.T) {
	input := validPlacementInput()
	if err := ValidatePlacementInput(&input, true); err != nil {
		t.Fatal(err)
	}
	if input.Currency != "USD" {
		t.Fatalf("expected uppercase currency, got %s", input.Currency)
	}
	if len(input.AllowedOrigins) != 1 || input.AllowedOrigins[0] != "https://publisher.test" {
		t.Fatalf("unexpected origins: %v", input.AllowedOrigins)
	}
}

func TestValidatePlacementRejectsBadInput(t *testing.T) {
	input := validPlacementInput()
	input.Width = 0
	if err := ValidatePlacementInput(&input, true); err == nil {
		t.Fatal("expected size rejection")
	}
	input = validPlacementInput()
	input.Currency = "US"
	if err := ValidatePlacementInput(&input, true); err == nil {
		t.Fatal("expected currency rejection")
	}
	input = validPlacementInput()
	input.AllowedOrigins = []string{"javascript:alert(1)"}
	if err := ValidatePlacementInput(&input, true); err == nil {
		t.Fatal("expected origin rejection")
	}
}

func TestValidateAuctionPolicyRejectsSecondPrice(t *testing.T) {
	input := AuctionPolicyInput{Name: "second price", At: 2}
	if err := ValidateAuctionPolicyInput(&input, true); err == nil || !strings.Contains(err.Error(), "at=1") {
		t.Fatalf("expected second-price rejection, got %v", err)
	}
	input = AuctionPolicyInput{Name: "first price"}
	if err := ValidateAuctionPolicyInput(&input, true); err != nil {
		t.Fatal(err)
	}
	if input.At != 1 || input.TieBreaker != "dsp_priority_then_ssp_hash" {
		t.Fatalf("unexpected defaults: %+v", input)
	}
}

func TestValidateDSPEndpointRequiresSSRFAllowlist(t *testing.T) {
	input := DSPEndpointInput{Name: "DSP", BidURL: "https://dsp.test/openrtb2/bid", TimeoutMS: 50}
	if err := ValidateDSPEndpointInput(&input, true); err == nil {
		t.Fatal("expected missing callback allowlist rejection")
	}
	input.CallbackHosts = []string{"DSP.test.", "https://bad/path"}
	if err := ValidateDSPEndpointInput(&input, true); err == nil {
		t.Fatal("expected invalid callback host rejection")
	}
	input.CallbackHosts = []string{"DSP.test.", "dsp.test"}
	if err := ValidateDSPEndpointInput(&input, true); err != nil {
		t.Fatal(err)
	}
	if len(input.CallbackHosts) != 1 || input.CallbackHosts[0] != "dsp.test" {
		t.Fatalf("unexpected normalized hosts: %v", input.CallbackHosts)
	}
	input.BidURL = "file:///etc/passwd"
	if err := ValidateDSPEndpointInput(&input, true); err == nil {
		t.Fatal("expected non-http bid URL rejection")
	}
}

func TestValidateCreativeRegistration(t *testing.T) {
	entry := CreativeRegistration{
		CreativeID: strings.Repeat("c", 32), AssetRef: "as:" + strings.Repeat("d", 32) + ":" + strings.Repeat("e", 64),
		AssetSHA256: strings.Repeat("e", 64), Width: 728, Height: 90, MIMEType: "image/png",
		LandingURL: "https://publisher.test/landing", Status: "active",
	}
	if err := ValidateCreativeRegistration(&entry); err != nil {
		t.Fatal(err)
	}
	if entry.LandingURLHost != "publisher.test" {
		t.Fatalf("expected derived landing host, got %s", entry.LandingURLHost)
	}
	bad := entry
	bad.AssetRef = "as:../../etc/passwd"
	if err := ValidateCreativeRegistration(&bad); err == nil {
		t.Fatal("expected unsafe asset ref rejection")
	}
	bad = entry
	bad.MIMEType = "text/html"
	if err := ValidateCreativeRegistration(&bad); err == nil {
		t.Fatal("expected unsupported mime rejection")
	}
	bad = entry
	bad.LandingURLHost = "evil.test"
	if err := ValidateCreativeRegistration(&bad); err == nil {
		t.Fatal("expected landing host mismatch rejection")
	}
}
