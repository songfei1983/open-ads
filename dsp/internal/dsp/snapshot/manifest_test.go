package snapshot

import (
	"errors"
	"testing"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

func TestRegisterKeepsManifestVersionImmutable(t *testing.T) {
	const version = "snapshot-immutable-test-v1"
	source := &Manifest{
		Version: version,
		Campaigns: map[string]domain.Campaign{
			"campaign-1": {ID: "campaign-1", LineItemIDs: []string{"line-1"}},
		},
		LineItems: map[string]domain.LineItem{
			"line-1": {ID: "line-1", CampaignID: "campaign-1", CreativeIDs: []string{"creative-1"}},
		},
		Creatives: map[string]domain.Creative{
			"creative-1": {ID: "creative-1"},
		},
	}
	if err := Register(source); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	source.Campaigns["campaign-1"].LineItemIDs[0] = "changed"
	source.LineItems["line-1"].CreativeIDs[0] = "changed"
	delete(source.Campaigns, "campaign-1")

	loaded, err := Load(version)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := loaded.Campaigns["campaign-1"].LineItemIDs[0]; got != "line-1" {
		t.Fatalf("stored campaign was mutated through source: got %q", got)
	}
	if got := loaded.LineItems["line-1"].CreativeIDs[0]; got != "creative-1" {
		t.Fatalf("stored line item was mutated through source: got %q", got)
	}

	loaded.Campaigns["campaign-1"].LineItemIDs[0] = "mutated-reader"
	delete(loaded.LineItems, "line-1")
	loadedAgain, err := Load(version)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if got := loadedAgain.Campaigns["campaign-1"].LineItemIDs[0]; got != "line-1" {
		t.Fatalf("stored campaign was mutated through loaded copy: got %q", got)
	}
	if _, ok := loadedAgain.LineItems["line-1"]; !ok {
		t.Fatal("stored line item was removed through loaded copy")
	}

	err = Register(&Manifest{Version: version})
	if !errors.Is(err, ErrVersionExists) {
		t.Fatalf("Register() duplicate error = %v, want ErrVersionExists", err)
	}
	if got := CurrentVersion(); got != version {
		t.Fatalf("CurrentVersion() = %q, want %q", got, version)
	}
}

func TestRegisterRejectsBrokenConfigurationReferences(t *testing.T) {
	err := Register(&Manifest{
		Version: "snapshot-invalid-test-v1",
		Campaigns: map[string]domain.Campaign{
			"campaign-1": {ID: "campaign-1", LineItemIDs: []string{"missing-line-item"}},
		},
	})
	if err == nil {
		t.Fatal("Register() accepted a campaign with a missing line item")
	}
	if got := CurrentVersion(); got == "snapshot-invalid-test-v1" {
		t.Fatal("invalid manifest became the active version")
	}
}
