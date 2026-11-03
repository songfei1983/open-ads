package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

// loadPublishedManifest builds the complete projection from the immutable
// published rows. Draft tables are never read on this path.
func (s *SQLStore) loadPublishedManifest(ctx context.Context, q queryer, version string) (*snapshot.Manifest, error) {
	rows, err := q.QueryContext(ctx, `SELECT aggregate_type, aggregate_id, config_version, payload
		FROM ssp_published_config WHERE tenant_id = ? ORDER BY aggregate_type, aggregate_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("read published SSP projection: %w", err)
	}
	defer rows.Close()

	manifest := &snapshot.Manifest{
		Version:         version,
		Publishers:      map[string]snapshot.Publisher{},
		Properties:      map[string]snapshot.Property{},
		AuctionPolicies: map[string]snapshot.AuctionPolicy{},
		Placements:      map[string]snapshot.Placement{},
		SupplyEndpoints: map[string]snapshot.SupplyEndpoint{},
		DSPEndpoints:    map[string]snapshot.DSPEndpoint{},
	}
	rowsSeen := 0
	for rows.Next() {
		var aggregateType string
		var id []byte
		var configVersion uint64
		var payload []byte
		if err := rows.Scan(&aggregateType, &id, &configVersion, &payload); err != nil {
			return nil, fmt.Errorf("scan published SSP projection: %w", err)
		}
		rowsSeen++
		idText := hex.EncodeToString(id)
		switch aggregateType {
		case AggregatePublisher:
			var value snapshot.Publisher
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published publisher: %w", err)
			}
			value.ID = idText
			manifest.Publishers[idText] = value
		case AggregateProperty:
			var value snapshot.Property
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published property: %w", err)
			}
			value.ID = idText
			manifest.Properties[idText] = value
		case AggregateAuctionPolicy:
			var value snapshot.AuctionPolicy
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published auction policy: %w", err)
			}
			value.ID = idText
			manifest.AuctionPolicies[idText] = value
		case AggregatePlacement:
			var value snapshot.Placement
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published placement: %w", err)
			}
			value.ID = idText
			manifest.Placements[idText] = value
		case AggregateSupplyEndpoint:
			var value snapshot.SupplyEndpoint
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published supply endpoint: %w", err)
			}
			value.ID = idText
			manifest.SupplyEndpoints[idText] = value
		case AggregateDSPEndpoint:
			var value snapshot.DSPEndpoint
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, fmt.Errorf("decode published dsp endpoint: %w", err)
			}
			value.ID = idText
			if value.CreativeWhitelist == nil {
				value.CreativeWhitelist = map[string]snapshot.Creative{}
			}
			manifest.DSPEndpoints[idText] = value
		default:
			return nil, fmt.Errorf("unknown published SSP resource type %q", aggregateType)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published SSP projection: %w", err)
	}
	if rowsSeen == 0 {
		return nil, ErrNoPublishedConfig
	}
	if err := snapshot.Validate(manifest); err != nil {
		return nil, fmt.Errorf("validate published SSP manifest: %w", err)
	}
	return manifest, nil
}

// LoadPublishedManifest loads the projection with the stored manifest version.
func (s *SQLStore) LoadPublishedManifest(ctx context.Context) (*snapshot.Manifest, error) {
	version, err := s.currentManifestVersion(ctx)
	if err != nil {
		return nil, err
	}
	if version == "" {
		version = snapshot.FormatVersion(1)
	}
	return s.loadPublishedManifest(ctx, s.db, version)
}

// RefreshPublished loads and atomically activates the current projection.
func (s *SQLStore) RefreshPublished(ctx context.Context) error {
	manifest, err := s.LoadPublishedManifest(ctx)
	if err != nil {
		return err
	}
	return snapshot.Activate(manifest)
}

func (s *SQLStore) currentManifestVersion(ctx context.Context) (string, error) {
	var version uint64
	err := s.db.QueryRowContext(ctx, `SELECT manifest_version FROM ssp_config_manifest WHERE tenant_id = ?`, s.tenantID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read SSP manifest version: %w", err)
	}
	return snapshot.FormatVersion(version), nil
}
