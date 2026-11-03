package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/domain"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

// CreatePublisher stores a new draft publisher.
func (s *SQLStore) CreatePublisher(ctx context.Context, input PublisherInput) (Publisher, error) {
	if err := ValidatePublisherInput(&input, true); err != nil {
		return Publisher{}, err
	}
	id, err := domain.NewUUIDv7()
	if err != nil {
		return Publisher{}, err
	}
	idBytes, _ := hex.DecodeString(id)
	_, err = s.db.ExecContext(ctx, `INSERT INTO publisher
		(tenant_id, publisher_id, name, status, version, published_version, created_at, updated_at)
		VALUES (?, ?, ?, 'draft', 1, 0, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		s.tenantID, idBytes, input.Name)
	if err != nil {
		return Publisher{}, mapWriteError(err)
	}
	return s.getPublisher(ctx, id)
}

// UpdatePublisher applies an optimistic-locking update to a draft publisher.
func (s *SQLStore) UpdatePublisher(ctx context.Context, id string, input PublisherInput) (Publisher, error) {
	if err := ValidatePublisherInput(&input, false); err != nil {
		return Publisher{}, err
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return Publisher{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE publisher
		SET name = ?, status = ?, version = version + 1, updated_at = UTC_TIMESTAMP(6)
		WHERE tenant_id = ? AND publisher_id = ? AND version = ?`,
		input.Name, input.Status, s.tenantID, idBytes, input.Version)
	if err != nil {
		return Publisher{}, mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Publisher{}, s.classifyMiss(ctx, `SELECT 1 FROM publisher WHERE tenant_id = ? AND publisher_id = ?`, s.tenantID, idBytes)
	}
	return s.getPublisher(ctx, id)
}

// CreateProperty stores a new draft property.
func (s *SQLStore) CreateProperty(ctx context.Context, input PropertyInput) (Property, error) {
	if err := ValidatePropertyInput(&input, true); err != nil {
		return Property{}, err
	}
	publisherID, err := decodeID(input.PublisherID)
	if err != nil {
		return Property{}, err
	}
	id, err := domain.NewUUIDv7()
	if err != nil {
		return Property{}, err
	}
	idBytes, _ := hex.DecodeString(id)
	_, err = s.db.ExecContext(ctx, `INSERT INTO property
		(tenant_id, property_id, publisher_id, name, property_type, external_id, status, version, published_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'draft', 1, 0, UTC_TIMESTAMP(6))`,
		s.tenantID, idBytes, publisherID, input.Name, input.Type, input.ExternalID)
	if err != nil {
		return Property{}, mapWriteError(err)
	}
	return s.getProperty(ctx, id)
}

// UpdateProperty applies an optimistic-locking update to a draft property.
func (s *SQLStore) UpdateProperty(ctx context.Context, id string, input PropertyInput) (Property, error) {
	if err := ValidatePropertyInput(&input, false); err != nil {
		return Property{}, err
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return Property{}, err
	}
	publisherID, err := decodeID(input.PublisherID)
	if err != nil {
		return Property{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE property
		SET publisher_id = ?, name = ?, property_type = ?, external_id = ?, status = ?,
		version = version + 1, updated_at = UTC_TIMESTAMP(6)
		WHERE tenant_id = ? AND property_id = ? AND version = ?`,
		publisherID, input.Name, input.Type, input.ExternalID, input.Status, s.tenantID, idBytes, input.Version)
	if err != nil {
		return Property{}, mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Property{}, s.classifyMiss(ctx, `SELECT 1 FROM property WHERE tenant_id = ? AND property_id = ?`, s.tenantID, idBytes)
	}
	return s.getProperty(ctx, id)
}

// CreateAuctionPolicy stores a new draft auction policy.
func (s *SQLStore) CreateAuctionPolicy(ctx context.Context, input AuctionPolicyInput) (AuctionPolicy, error) {
	if err := ValidateAuctionPolicyInput(&input, true); err != nil {
		return AuctionPolicy{}, err
	}
	id, err := domain.NewUUIDv7()
	if err != nil {
		return AuctionPolicy{}, err
	}
	idBytes, _ := hex.DecodeString(id)
	_, err = s.db.ExecContext(ctx, `INSERT INTO auction_policy
		(tenant_id, auction_policy_id, name, auction_type, tie_breaker, floor_enforcement, currency_policy, status, version, published_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'draft', 1, 0, UTC_TIMESTAMP(6))`,
		s.tenantID, idBytes, input.Name, input.At, input.TieBreaker, input.FloorEnforcement, input.CurrencyPolicy)
	if err != nil {
		return AuctionPolicy{}, mapWriteError(err)
	}
	return s.getAuctionPolicy(ctx, id)
}

// UpdateAuctionPolicy applies an optimistic-locking update to a draft policy.
func (s *SQLStore) UpdateAuctionPolicy(ctx context.Context, id string, input AuctionPolicyInput) (AuctionPolicy, error) {
	if err := ValidateAuctionPolicyInput(&input, false); err != nil {
		return AuctionPolicy{}, err
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return AuctionPolicy{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auction_policy
		SET name = ?, auction_type = ?, tie_breaker = ?, floor_enforcement = ?, currency_policy = ?,
		version = version + 1, updated_at = UTC_TIMESTAMP(6)
		WHERE tenant_id = ? AND auction_policy_id = ? AND version = ?`,
		input.Name, input.At, input.TieBreaker, input.FloorEnforcement, input.CurrencyPolicy,
		s.tenantID, idBytes, input.Version)
	if err != nil {
		return AuctionPolicy{}, mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return AuctionPolicy{}, s.classifyMiss(ctx, `SELECT 1 FROM auction_policy WHERE tenant_id = ? AND auction_policy_id = ?`, s.tenantID, idBytes)
	}
	return s.getAuctionPolicy(ctx, id)
}

// CreatePlacement stores a new draft placement.
func (s *SQLStore) CreatePlacement(ctx context.Context, input PlacementInput) (Placement, error) {
	if err := ValidatePlacementInput(&input, true); err != nil {
		return Placement{}, err
	}
	propertyID, err := decodeID(input.PropertyID)
	if err != nil {
		return Placement{}, err
	}
	policyID, err := decodeID(input.AuctionPolicyID)
	if err != nil {
		return Placement{}, err
	}
	id, err := domain.NewUUIDv7()
	if err != nil {
		return Placement{}, err
	}
	idBytes, _ := hex.DecodeString(id)
	_, err = s.db.ExecContext(ctx, `INSERT INTO placement
		(tenant_id, placement_id, property_id, auction_policy_id, name, media_type, width_px, height_px,
		 floor_cpm_micros, currency, privacy_policy, allowed_origins, status, version, published_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', 1, 0, UTC_TIMESTAMP(6))`,
		s.tenantID, idBytes, propertyID, policyID, input.Name, input.MediaType, input.Width, input.Height,
		input.FloorCPMMicros, input.Currency, input.PrivacyPolicy, encodeOrigins(input.AllowedOrigins))
	if err != nil {
		return Placement{}, mapWriteError(err)
	}
	return s.getPlacement(ctx, id)
}

// UpdatePlacement applies an optimistic-locking update to a draft placement.
func (s *SQLStore) UpdatePlacement(ctx context.Context, id string, input PlacementInput) (Placement, error) {
	if err := ValidatePlacementInput(&input, false); err != nil {
		return Placement{}, err
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return Placement{}, err
	}
	propertyID, err := decodeID(input.PropertyID)
	if err != nil {
		return Placement{}, err
	}
	policyID, err := decodeID(input.AuctionPolicyID)
	if err != nil {
		return Placement{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE placement
		SET property_id = ?, auction_policy_id = ?, name = ?, media_type = ?, width_px = ?, height_px = ?,
		floor_cpm_micros = ?, currency = ?, privacy_policy = ?, allowed_origins = ?, status = ?,
		version = version + 1, updated_at = UTC_TIMESTAMP(6)
		WHERE tenant_id = ? AND placement_id = ? AND version = ?`,
		propertyID, policyID, input.Name, input.MediaType, input.Width, input.Height,
		input.FloorCPMMicros, input.Currency, input.PrivacyPolicy, encodeOrigins(input.AllowedOrigins), input.Status,
		s.tenantID, idBytes, input.Version)
	if err != nil {
		return Placement{}, mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Placement{}, s.classifyMiss(ctx, `SELECT 1 FROM placement WHERE tenant_id = ? AND placement_id = ?`, s.tenantID, idBytes)
	}
	return s.getPlacement(ctx, id)
}

// CreateSupplyEndpoint stores a new draft Supply profile.
func (s *SQLStore) CreateSupplyEndpoint(ctx context.Context, input SupplyEndpointInput) (SupplyEndpoint, error) {
	if err := ValidateSupplyEndpointInput(&input, true); err != nil {
		return SupplyEndpoint{}, err
	}
	propertyID, err := decodeID(input.PropertyID)
	if err != nil {
		return SupplyEndpoint{}, err
	}
	id, err := domain.NewUUIDv7()
	if err != nil {
		return SupplyEndpoint{}, err
	}
	idBytes, _ := hex.DecodeString(id)
	var credential []byte
	if input.Credential != "" {
		credential, _ = hex.DecodeString(snapshot.HashSHA256Hex(input.Credential))
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO supply_endpoint
		(tenant_id, endpoint_id, property_id, name, protocol_version, credential_sha256, allowed_origins,
		 rate_limit_per_second, status, version, published_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', 1, 0)`,
		s.tenantID, idBytes, propertyID, input.Name, input.ProtocolVersion, credential,
		encodeOrigins(input.AllowedOrigins), input.RateLimitPerSecond)
	if err != nil {
		return SupplyEndpoint{}, mapWriteError(err)
	}
	return s.getSupplyEndpoint(ctx, id)
}

// UpdateSupplyEndpoint applies an optimistic-locking update to a draft profile.
func (s *SQLStore) UpdateSupplyEndpoint(ctx context.Context, id string, input SupplyEndpointInput) (SupplyEndpoint, error) {
	if err := ValidateSupplyEndpointInput(&input, false); err != nil {
		return SupplyEndpoint{}, err
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return SupplyEndpoint{}, err
	}
	propertyID, err := decodeID(input.PropertyID)
	if err != nil {
		return SupplyEndpoint{}, err
	}
	query := `UPDATE supply_endpoint
		SET property_id = ?, name = ?, protocol_version = ?, allowed_origins = ?, rate_limit_per_second = ?,
		status = ?, version = version + 1
		WHERE tenant_id = ? AND endpoint_id = ? AND version = ?`
	args := []any{propertyID, input.Name, input.ProtocolVersion, encodeOrigins(input.AllowedOrigins),
		input.RateLimitPerSecond, input.Status, s.tenantID, idBytes, input.Version}
	if input.Credential != "" {
		credential, _ := hex.DecodeString(snapshot.HashSHA256Hex(input.Credential))
		query = `UPDATE supply_endpoint
			SET property_id = ?, name = ?, protocol_version = ?, credential_sha256 = ?, allowed_origins = ?,
			rate_limit_per_second = ?, status = ?, version = version + 1
			WHERE tenant_id = ? AND endpoint_id = ? AND version = ?`
		args = []any{propertyID, input.Name, input.ProtocolVersion, credential, encodeOrigins(input.AllowedOrigins),
			input.RateLimitPerSecond, input.Status, s.tenantID, idBytes, input.Version}
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return SupplyEndpoint{}, mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return SupplyEndpoint{}, s.classifyMiss(ctx, `SELECT 1 FROM supply_endpoint WHERE tenant_id = ? AND endpoint_id = ?`, s.tenantID, idBytes)
	}
	return s.getSupplyEndpoint(ctx, id)
}

// CreateDSPEndpoint stores a new draft bidder endpoint.
func (s *SQLStore) CreateDSPEndpoint(ctx context.Context, input DSPEndpointInput) (DSPEndpoint, error) {
	if err := ValidateDSPEndpointInput(&input, true); err != nil {
		return DSPEndpoint{}, err
	}
	id, err := domain.NewUUIDv7()
	if err != nil {
		return DSPEndpoint{}, err
	}
	idBytes, _ := hex.DecodeString(id)
	callbacks, _ := jsonCallbacks(input.CallbackHosts)
	_, err = s.db.ExecContext(ctx, `INSERT INTO dsp_endpoint
		(tenant_id, dsp_endpoint_id, name, bid_url, openrtb_profile, callback_host_allowlist, priority, timeout_ms, status, version, published_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', 1, 0, UTC_TIMESTAMP(6))`,
		s.tenantID, idBytes, input.Name, input.BidURL, input.OpenRTBProfile, callbacks, input.Priority, input.TimeoutMS)
	if err != nil {
		return DSPEndpoint{}, mapWriteError(err)
	}
	return s.getDSPEndpoint(ctx, id)
}

// UpdateDSPEndpoint applies an optimistic-locking update to a draft bidder.
func (s *SQLStore) UpdateDSPEndpoint(ctx context.Context, id string, input DSPEndpointInput) (DSPEndpoint, error) {
	if err := ValidateDSPEndpointInput(&input, false); err != nil {
		return DSPEndpoint{}, err
	}
	idBytes, err := decodeID(id)
	if err != nil {
		return DSPEndpoint{}, err
	}
	callbacks, _ := jsonCallbacks(input.CallbackHosts)
	result, err := s.db.ExecContext(ctx, `UPDATE dsp_endpoint
		SET name = ?, bid_url = ?, openrtb_profile = ?, callback_host_allowlist = ?, priority = ?,
		timeout_ms = ?, status = ?, version = version + 1, updated_at = UTC_TIMESTAMP(6)
		WHERE tenant_id = ? AND dsp_endpoint_id = ? AND version = ?`,
		input.Name, input.BidURL, input.OpenRTBProfile, callbacks, input.Priority, input.TimeoutMS,
		input.Status, s.tenantID, idBytes, input.Version)
	if err != nil {
		return DSPEndpoint{}, mapWriteError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return DSPEndpoint{}, s.classifyMiss(ctx, `SELECT 1 FROM dsp_endpoint WHERE tenant_id = ? AND dsp_endpoint_id = ?`, s.tenantID, idBytes)
	}
	return s.getDSPEndpoint(ctx, id)
}

func (s *SQLStore) classifyMiss(ctx context.Context, query string, args ...any) error {
	var one int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrVersionConflict
}

func (s *SQLStore) getPublisher(ctx context.Context, id string) (Publisher, error) {
	idBytes, err := decodeID(id)
	if err != nil {
		return Publisher{}, err
	}
	var item Publisher
	err = s.db.QueryRowContext(ctx, `SELECT publisher_id, name, status, version, published_version
		FROM publisher WHERE tenant_id = ? AND publisher_id = ?`, s.tenantID, idBytes).
		Scan(&idBytes, &item.Name, &item.Status, &item.Version, &item.PublishedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Publisher{}, ErrNotFound
	}
	if err != nil {
		return Publisher{}, fmt.Errorf("get publisher: %w", err)
	}
	item.ID = hex.EncodeToString(idBytes)
	return item, nil
}

func (s *SQLStore) getProperty(ctx context.Context, id string) (Property, error) {
	idBytes, err := decodeID(id)
	if err != nil {
		return Property{}, err
	}
	var publisherID []byte
	var item Property
	err = s.db.QueryRowContext(ctx, `SELECT property_id, publisher_id, name, property_type, external_id, status, version, published_version
		FROM property WHERE tenant_id = ? AND property_id = ?`, s.tenantID, idBytes).
		Scan(&idBytes, &publisherID, &item.Name, &item.Type, &item.ExternalID, &item.Status, &item.Version, &item.PublishedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Property{}, ErrNotFound
	}
	if err != nil {
		return Property{}, fmt.Errorf("get property: %w", err)
	}
	item.ID, item.PublisherID = hex.EncodeToString(idBytes), hex.EncodeToString(publisherID)
	return item, nil
}

func (s *SQLStore) getAuctionPolicy(ctx context.Context, id string) (AuctionPolicy, error) {
	idBytes, err := decodeID(id)
	if err != nil {
		return AuctionPolicy{}, err
	}
	var item AuctionPolicy
	err = s.db.QueryRowContext(ctx, `SELECT auction_policy_id, name, auction_type, tie_breaker, floor_enforcement, currency_policy, status, version, published_version
		FROM auction_policy WHERE tenant_id = ? AND auction_policy_id = ?`, s.tenantID, idBytes).
		Scan(&idBytes, &item.Name, &item.At, &item.TieBreaker, &item.FloorEnforcement, &item.CurrencyPolicy, &item.Status, &item.Version, &item.PublishedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return AuctionPolicy{}, ErrNotFound
	}
	if err != nil {
		return AuctionPolicy{}, fmt.Errorf("get auction policy: %w", err)
	}
	item.ID = hex.EncodeToString(idBytes)
	return item, nil
}

func (s *SQLStore) getPlacement(ctx context.Context, id string) (Placement, error) {
	idBytes, err := decodeID(id)
	if err != nil {
		return Placement{}, err
	}
	var propertyID, policyID []byte
	var origins sql.NullString
	var item Placement
	err = s.db.QueryRowContext(ctx, `SELECT placement_id, property_id, auction_policy_id, name, media_type, width_px, height_px,
		floor_cpm_micros, currency, privacy_policy, allowed_origins, status, version, published_version
		FROM placement WHERE tenant_id = ? AND placement_id = ?`, s.tenantID, idBytes).
		Scan(&idBytes, &propertyID, &policyID, &item.Name, &item.MediaType, &item.Width, &item.Height,
			&item.FloorCPMMicros, &item.Currency, &item.PrivacyPolicy, &origins, &item.Status, &item.Version, &item.PublishedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Placement{}, ErrNotFound
	}
	if err != nil {
		return Placement{}, fmt.Errorf("get placement: %w", err)
	}
	item.ID, item.PropertyID, item.AuctionPolicyID = hex.EncodeToString(idBytes), hex.EncodeToString(propertyID), hex.EncodeToString(policyID)
	item.AllowedOrigins = decodeOrigins(origins)
	return item, nil
}

func (s *SQLStore) getSupplyEndpoint(ctx context.Context, id string) (SupplyEndpoint, error) {
	idBytes, err := decodeID(id)
	if err != nil {
		return SupplyEndpoint{}, err
	}
	var propertyID, credential []byte
	var origins sql.NullString
	var item SupplyEndpoint
	err = s.db.QueryRowContext(ctx, `SELECT endpoint_id, property_id, name, protocol_version, credential_sha256,
		allowed_origins, rate_limit_per_second, status, version, published_version
		FROM supply_endpoint WHERE tenant_id = ? AND endpoint_id = ?`, s.tenantID, idBytes).
		Scan(&idBytes, &propertyID, &item.Name, &item.ProtocolVersion, &credential,
			&origins, &item.RateLimitPerSecond, &item.Status, &item.Version, &item.PublishedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return SupplyEndpoint{}, ErrNotFound
	}
	if err != nil {
		return SupplyEndpoint{}, fmt.Errorf("get supply endpoint: %w", err)
	}
	item.ID, item.PropertyID = hex.EncodeToString(idBytes), hex.EncodeToString(propertyID)
	item.CredentialConfigured = len(credential) == 32
	item.AllowedOrigins = decodeOrigins(origins)
	return item, nil
}

func (s *SQLStore) getDSPEndpoint(ctx context.Context, id string) (DSPEndpoint, error) {
	idBytes, err := decodeID(id)
	if err != nil {
		return DSPEndpoint{}, err
	}
	var callbacks []byte
	var item DSPEndpoint
	err = s.db.QueryRowContext(ctx, `SELECT dsp_endpoint_id, name, bid_url, openrtb_profile, callback_host_allowlist,
		priority, timeout_ms, status, version, published_version
		FROM dsp_endpoint WHERE tenant_id = ? AND dsp_endpoint_id = ?`, s.tenantID, idBytes).
		Scan(&idBytes, &item.Name, &item.BidURL, &item.OpenRTBProfile, &callbacks,
			&item.Priority, &item.TimeoutMS, &item.Status, &item.Version, &item.PublishedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return DSPEndpoint{}, ErrNotFound
	}
	if err != nil {
		return DSPEndpoint{}, fmt.Errorf("get dsp endpoint: %w", err)
	}
	item.ID = hex.EncodeToString(idBytes)
	_ = json.Unmarshal(callbacks, &item.CallbackHosts)
	if item.CallbackHosts == nil {
		item.CallbackHosts = []string{}
	}
	return item, nil
}
