package snapshot

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
)

var (
	ErrVersionExists = errors.New("manifest version already exists")
	ErrEmptyVersion  = errors.New("manifest version cannot be empty")
)

// Manifest is a versioned view of published ad configuration. Values returned
// by Load are detached copies so callers cannot mutate the registered version.
type Manifest struct {
	Version   string
	Campaigns map[string]domain.Campaign
	LineItems map[string]domain.LineItem
	Creatives map[string]domain.Creative
}

var manifestStore = struct {
	sync.RWMutex
	byVersion map[string]*Manifest
	current   string
}{byVersion: make(map[string]*Manifest)}

// Load returns a detached copy of a registered manifest. Missing versions are
// reported locally; this package never falls back to a database or remote store.
func Load(version string) (*Manifest, error) {
	manifestStore.RLock()
	m, ok := manifestStore.byVersion[version]
	if ok {
		m = clone(m)
	}
	manifestStore.RUnlock()
	if !ok {
		return nil, fmt.Errorf("manifest version %q not found", version)
	}
	return m, nil
}

// LoadCurrent returns the currently active published manifest and its version.
func LoadCurrent() (*Manifest, string, error) {
	manifestStore.RLock()
	version := manifestStore.current
	m, ok := manifestStore.byVersion[version]
	if ok {
		m = clone(m)
	}
	manifestStore.RUnlock()
	if !ok {
		return nil, "", errors.New("no active manifest")
	}
	return m, version, nil
}

// CurrentVersion returns the active version without exposing the stored
// manifest. An empty result means no snapshot has been published in this process.
func CurrentVersion() string {
	manifestStore.RLock()
	defer manifestStore.RUnlock()
	return manifestStore.current
}

// Register adds a new immutable version and atomically makes it current. A
// version can never be overwritten, so readers observe either the previous
// complete snapshot or the newly registered complete snapshot.
func Register(m *Manifest) error {
	if m == nil {
		return errors.New("manifest cannot be nil")
	}
	if m.Version == "" {
		return ErrEmptyVersion
	}
	if err := Validate(m); err != nil {
		return err
	}
	immutable := clone(m)
	manifestStore.Lock()
	defer manifestStore.Unlock()
	if _, exists := manifestStore.byVersion[m.Version]; exists {
		return fmt.Errorf("%w: %q", ErrVersionExists, m.Version)
	}
	manifestStore.byVersion[m.Version] = immutable
	manifestStore.current = m.Version
	return nil
}

// Validate checks that all published references resolve within the manifest.
// Iterating sorted keys keeps validation errors stable across runs.
func Validate(m *Manifest) error {
	if m == nil {
		return errors.New("manifest cannot be nil")
	}
	if m.Version == "" {
		return ErrEmptyVersion
	}

	campaignIDs := sortedKeys(m.Campaigns)
	for _, campaignID := range campaignIDs {
		campaign := m.Campaigns[campaignID]
		if campaign.ID == "" || campaign.ID != campaignID {
			return fmt.Errorf("campaign key %q does not match its ID", campaignID)
		}
		seen := make(map[string]struct{}, len(campaign.LineItemIDs))
		for _, lineItemID := range campaign.LineItemIDs {
			if _, duplicate := seen[lineItemID]; duplicate {
				return fmt.Errorf("campaign %q references line item %q more than once", campaignID, lineItemID)
			}
			seen[lineItemID] = struct{}{}
			lineItem, ok := m.LineItems[lineItemID]
			if !ok || lineItem.CampaignID != campaignID {
				return fmt.Errorf("campaign %q references invalid line item %q", campaignID, lineItemID)
			}
		}
	}

	for _, lineItemID := range sortedKeys(m.LineItems) {
		lineItem := m.LineItems[lineItemID]
		if lineItem.ID == "" || lineItem.ID != lineItemID {
			return fmt.Errorf("line item key %q does not match its ID", lineItemID)
		}
		campaign, ok := m.Campaigns[lineItem.CampaignID]
		if !ok || !contains(campaign.LineItemIDs, lineItemID) {
			return fmt.Errorf("line item %q is not linked from campaign %q", lineItemID, lineItem.CampaignID)
		}
		seen := make(map[string]struct{}, len(lineItem.CreativeIDs))
		for _, creativeID := range lineItem.CreativeIDs {
			if _, duplicate := seen[creativeID]; duplicate {
				return fmt.Errorf("line item %q references creative %q more than once", lineItemID, creativeID)
			}
			seen[creativeID] = struct{}{}
			if _, ok := m.Creatives[creativeID]; !ok {
				return fmt.Errorf("line item %q references missing creative %q", lineItemID, creativeID)
			}
		}
	}

	for _, creativeID := range sortedKeys(m.Creatives) {
		if m.Creatives[creativeID].ID != creativeID || creativeID == "" {
			return fmt.Errorf("creative key %q does not match its ID", creativeID)
		}
	}
	return nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Refresh registers and activates a fully constructed manifest. The version
// must be new; callers should build and validate configuration before calling.
func Refresh(version string) (*Manifest, error) {
	if version == "" {
		return nil, ErrEmptyVersion
	}
	m := &Manifest{Version: version, Campaigns: make(map[string]domain.Campaign)}
	if err := Register(m); err != nil {
		return nil, err
	}
	return clone(m), nil
}

func clone(m *Manifest) *Manifest {
	copy := &Manifest{
		Version:   m.Version,
		Campaigns: make(map[string]domain.Campaign, len(m.Campaigns)),
		LineItems: make(map[string]domain.LineItem, len(m.LineItems)),
		Creatives: make(map[string]domain.Creative, len(m.Creatives)),
	}
	for id, campaign := range m.Campaigns {
		campaign.LineItemIDs = append([]string(nil), campaign.LineItemIDs...)
		copy.Campaigns[id] = campaign
	}
	for id, lineItem := range m.LineItems {
		lineItem.CreativeIDs = append([]string(nil), lineItem.CreativeIDs...)
		copy.LineItems[id] = lineItem
	}
	for id, creative := range m.Creatives {
		copy.Creatives[id] = creative
	}
	return copy
}
