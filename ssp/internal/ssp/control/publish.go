package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

// Aggregate identifiers used by the outbox and the published projection.
const (
	AggregatePublisher      = "publisher"
	AggregateProperty       = "property"
	AggregateAuctionPolicy  = "auction_policy"
	AggregatePlacement      = "placement"
	AggregateSupplyEndpoint = "supply_endpoint"
	AggregateDSPEndpoint    = "dsp_endpoint"
)

type aggregateSpec struct {
	table     string
	idColumn  string
	statusCol string
}

func specFor(aggregateType string) (aggregateSpec, bool) {
	switch aggregateType {
	case AggregatePublisher:
		return aggregateSpec{"publisher", "publisher_id", "status"}, true
	case AggregateProperty:
		return aggregateSpec{"property", "property_id", "status"}, true
	case AggregateAuctionPolicy:
		return aggregateSpec{"auction_policy", "auction_policy_id", "status"}, true
	case AggregatePlacement:
		return aggregateSpec{"placement", "placement_id", "status"}, true
	case AggregateSupplyEndpoint:
		return aggregateSpec{"supply_endpoint", "endpoint_id", "status"}, true
	case AggregateDSPEndpoint:
		return aggregateSpec{"dsp_endpoint", "dsp_endpoint_id", "status"}, true
	default:
		return aggregateSpec{}, false
	}
}

// Publish validates the aggregate and its references, advances its version and
// writes both the immutable projection and an outbox event in one transaction.
func (s *SQLStore) Publish(ctx context.Context, aggregateType, id string, requestedVersion uint64) (string, error) {
	spec, ok := specFor(aggregateType)
	if !ok {
		return "", &apiError{status: 404, code: CodeNotFound, message: "unknown resource type"}
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", fmt.Errorf("begin publish: %w", err)
	}
	defer tx.Rollback()

	var currentVersion uint64
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT version FROM %s WHERE tenant_id = ? AND %s = ? FOR UPDATE`,
		spec.table, spec.idColumn), s.tenantID, idBytes)
	if err := row.Scan(&currentVersion); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("lock resource for publish: %w", err)
	}
	if requestedVersion != 0 && requestedVersion != currentVersion {
		return "", ErrVersionConflict
	}
	if err := s.validatePublish(ctx, tx, aggregateType, idBytes); err != nil {
		return "", err
	}
	newVersion := currentVersion + 1
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s
		SET status = 'active', version = ?, published_version = ?, updated_at = UTC_TIMESTAMP(6)
		WHERE tenant_id = ? AND %s = ?`, spec.table, spec.idColumn),
		newVersion, newVersion, s.tenantID, idBytes)
	if err != nil {
		return "", mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return "", ErrNotFound
	}
	payload, err := s.buildPublishedPayload(ctx, tx, aggregateType, idBytes, newVersion)
	if err != nil {
		return "", err
	}
	if err := s.writePublishedConfig(ctx, tx, aggregateType, idBytes, newVersion, payload); err != nil {
		return "", err
	}
	if err := s.writeOutbox(ctx, tx, aggregateType, idBytes, newVersion); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit publish: %w", err)
	}
	return snapshot.FormatVersion(newVersion), nil
}

func (s *SQLStore) validatePublish(ctx context.Context, tx *sql.Tx, aggregateType string, id []byte) error {
	requirePublished := func(query string, args ...any) error {
		var published uint64
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&published); errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidReference
		} else if err != nil {
			return err
		}
		if published == 0 {
			return ErrInvalidReference
		}
		return nil
	}
	switch aggregateType {
	case AggregatePublisher:
		return nil
	case AggregateProperty:
		var publisherID []byte
		if err := tx.QueryRowContext(ctx, `SELECT publisher_id FROM property WHERE tenant_id = ? AND property_id = ?`,
			s.tenantID, id).Scan(&publisherID); err != nil {
			return err
		}
		return requirePublished(`SELECT published_version FROM publisher WHERE tenant_id = ? AND publisher_id = ?`, s.tenantID, publisherID)
	case AggregateAuctionPolicy:
		return nil
	case AggregatePlacement:
		var propertyID, policyID []byte
		if err := tx.QueryRowContext(ctx, `SELECT property_id, auction_policy_id FROM placement WHERE tenant_id = ? AND placement_id = ?`,
			s.tenantID, id).Scan(&propertyID, &policyID); err != nil {
			return err
		}
		if err := requirePublished(`SELECT published_version FROM property WHERE tenant_id = ? AND property_id = ?`, s.tenantID, propertyID); err != nil {
			return err
		}
		return requirePublished(`SELECT published_version FROM auction_policy WHERE tenant_id = ? AND auction_policy_id = ?`, s.tenantID, policyID)
	case AggregateSupplyEndpoint:
		var propertyID []byte
		var credential []byte
		var protocol string
		if err := tx.QueryRowContext(ctx, `SELECT property_id, credential_sha256, protocol_version FROM supply_endpoint
			WHERE tenant_id = ? AND endpoint_id = ?`, s.tenantID, id).Scan(&propertyID, &credential, &protocol); err != nil {
			return err
		}
		if protocol != "openrtb2.6" {
			return &apiError{status: 422, code: CodeUnsupportedProfile, message: "only openrtb2.6 is supported"}
		}
		if len(credential) != 32 {
			return &apiError{status: 422, code: CodeBusinessRule, message: "a Supply credential is required before publishing"}
		}
		return requirePublished(`SELECT published_version FROM property WHERE tenant_id = ? AND property_id = ?`, s.tenantID, propertyID)
	case AggregateDSPEndpoint:
		var callbacks []byte
		if err := tx.QueryRowContext(ctx, `SELECT callback_host_allowlist FROM dsp_endpoint
			WHERE tenant_id = ? AND dsp_endpoint_id = ?`, s.tenantID, id).Scan(&callbacks); err != nil {
			return err
		}
		var hosts []string
		_ = json.Unmarshal(callbacks, &hosts)
		if len(hosts) == 0 {
			return &apiError{status: 422, code: CodeBusinessRule, message: "a callback host allowlist is required before publishing"}
		}
		return nil
	default:
		return &apiError{status: 404, code: CodeNotFound, message: "unknown resource type"}
	}
}

func (s *SQLStore) buildPublishedPayload(ctx context.Context, tx *sql.Tx, aggregateType string, id []byte, version uint64) (any, error) {
	idText := hex.EncodeToString(id)
	switch aggregateType {
	case AggregatePublisher:
		var name, status string
		if err := tx.QueryRowContext(ctx, `SELECT name, status FROM publisher WHERE tenant_id = ? AND publisher_id = ?`,
			s.tenantID, id).Scan(&name, &status); err != nil {
			return nil, err
		}
		return snapshot.Publisher{ID: idText, Name: name, Status: status, Version: version}, nil
	case AggregateProperty:
		var publisherID []byte
		var name, propertyType, externalID, status string
		if err := tx.QueryRowContext(ctx, `SELECT publisher_id, name, property_type, external_id, status
			FROM property WHERE tenant_id = ? AND property_id = ?`, s.tenantID, id).
			Scan(&publisherID, &name, &propertyType, &externalID, &status); err != nil {
			return nil, err
		}
		return snapshot.Property{ID: idText, PublisherID: hex.EncodeToString(publisherID), Name: name,
			Type: propertyType, ExternalID: externalID, Status: status, Version: version}, nil
	case AggregateAuctionPolicy:
		var name, tieBreaker, floorEnforcement, currencyPolicy, status string
		var at int
		if err := tx.QueryRowContext(ctx, `SELECT name, auction_type, tie_breaker, floor_enforcement, currency_policy, status
			FROM auction_policy WHERE tenant_id = ? AND auction_policy_id = ?`, s.tenantID, id).
			Scan(&name, &at, &tieBreaker, &floorEnforcement, &currencyPolicy, &status); err != nil {
			return nil, err
		}
		return snapshot.AuctionPolicy{ID: idText, Name: name, At: at, TieBreaker: tieBreaker,
			FloorEnforcement: floorEnforcement, CurrencyPolicy: currencyPolicy, Status: status, Version: version}, nil
	case AggregatePlacement:
		var propertyID, policyID []byte
		var name, mediaType, currency, privacy, status string
		var width, height int
		var floor int64
		var origins sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT property_id, auction_policy_id, name, media_type, width_px, height_px,
			floor_cpm_micros, currency, privacy_policy, allowed_origins, status
			FROM placement WHERE tenant_id = ? AND placement_id = ?`, s.tenantID, id).
			Scan(&propertyID, &policyID, &name, &mediaType, &width, &height, &floor, &currency, &privacy, &origins, &status); err != nil {
			return nil, err
		}
		return snapshot.Placement{ID: idText, PropertyID: hex.EncodeToString(propertyID), AuctionPolicyID: hex.EncodeToString(policyID),
			Name: name, MediaType: mediaType, Width: width, Height: height, FloorCPMMicros: floor,
			Currency: currency, PrivacyPolicy: privacy, AllowedOrigins: decodeOrigins(origins), Status: status, Version: version}, nil
	case AggregateSupplyEndpoint:
		var propertyID, credential []byte
		var name, protocol, status string
		var origins sql.NullString
		var rate int
		if err := tx.QueryRowContext(ctx, `SELECT property_id, name, protocol_version, credential_sha256, allowed_origins,
			rate_limit_per_second, status FROM supply_endpoint WHERE tenant_id = ? AND endpoint_id = ?`, s.tenantID, id).
			Scan(&propertyID, &name, &protocol, &credential, &origins, &rate, &status); err != nil {
			return nil, err
		}
		return snapshot.SupplyEndpoint{ID: idText, PropertyID: hex.EncodeToString(propertyID), Name: name,
			ProtocolVersion: protocol, CredentialSHA256: hex.EncodeToString(credential), AllowedOrigins: decodeOrigins(origins),
			RateLimitPerSecond: rate, Status: status, Version: version}, nil
	case AggregateDSPEndpoint:
		var name, bidURL, profile, status string
		var callbacks []byte
		var priority, timeout int
		if err := tx.QueryRowContext(ctx, `SELECT name, bid_url, openrtb_profile, callback_host_allowlist, priority, timeout_ms, status
			FROM dsp_endpoint WHERE tenant_id = ? AND dsp_endpoint_id = ?`, s.tenantID, id).
			Scan(&name, &bidURL, &profile, &callbacks, &priority, &timeout, &status); err != nil {
			return nil, err
		}
		var hosts []string
		_ = json.Unmarshal(callbacks, &hosts)
		creatives, err := s.loadWhitelist(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return snapshot.DSPEndpoint{ID: idText, Name: name, BidURL: bidURL, OpenRTBProfile: profile,
			CallbackHosts: hosts, Priority: priority, TimeoutMS: timeout, Status: status,
			Version: version, CreativeWhitelist: creatives}, nil
	default:
		return nil, &apiError{status: 404, code: CodeNotFound, message: "unknown resource type"}
	}
}

func (s *SQLStore) writePublishedConfig(ctx context.Context, tx *sql.Tx, aggregateType string, id []byte, version uint64, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode published config: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ssp_published_config
		(tenant_id, aggregate_type, aggregate_id, config_version, payload, published_at)
		VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(6))
		ON DUPLICATE KEY UPDATE config_version = VALUES(config_version), payload = VALUES(payload), published_at = VALUES(published_at)`,
		s.tenantID, aggregateType, id, version, encoded)
	if err != nil {
		return mapWriteError(err)
	}
	return nil
}

func (s *SQLStore) writeOutbox(ctx context.Context, tx *sql.Tx, aggregateType string, id []byte, version uint64) error {
	eventID, err := domain.NewUUIDv7()
	if err != nil {
		return err
	}
	eventBytes, _ := hex.DecodeString(eventID)
	_, err = tx.ExecContext(ctx, `INSERT INTO ssp_config_outbox
		(event_id, tenant_id, aggregate_type, aggregate_id, aggregate_version, created_at)
		VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(6))`, eventBytes, s.tenantID, aggregateType, id, version)
	if err != nil {
		return mapWriteError(err)
	}
	return nil
}

// PublishConfigVersion returns the formatted version for an aggregate.
func PublishConfigVersion(version uint64) string { return snapshot.FormatVersion(version) }

// normalizeAggregate maps URL resource names to aggregate types.
func NormalizeAggregateType(resource string) (string, bool) {
	resource = strings.ToLower(strings.TrimSpace(resource))
	switch resource {
	case "publishers":
		return AggregatePublisher, true
	case "properties":
		return AggregateProperty, true
	case "auction-policies":
		return AggregateAuctionPolicy, true
	case "placements":
		return AggregatePlacement, true
	case "supply-endpoints":
		return AggregateSupplyEndpoint, true
	case "dsp-endpoints":
		return AggregateDSPEndpoint, true
	default:
		return "", false
	}
}
