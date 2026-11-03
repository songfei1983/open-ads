// Package control implements the SSP management plane, its versioned publish
// pipeline and the outbox that fans configuration out to Valkey.
package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

// Sentinel errors mapped by the HTTP handler onto the API contract.
var (
	ErrNotFound          = errors.New("resource not found")
	ErrVersionConflict   = errors.New("resource version conflict")
	ErrInvalidReference  = errors.New("referenced resource is not published")
	ErrNoPublishedConfig = errors.New("no published SSP configuration")
	ErrRegistryNotReady  = errors.New("dsp endpoint has no published configuration")
)

// SQLStore is the tenant-scoped MariaDB control plane store.
type SQLStore struct {
	db        *sql.DB
	tenantID  []byte
	tenantHex string
}

// NewSQLStore validates the tenant and returns a store bound to it.
func NewSQLStore(db *sql.DB, tenantID string) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("ssp control store requires a database")
	}
	tenant := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tenantID), "-", ""))
	if err := snapshot.ValidateID(tenant); err != nil {
		return nil, fmt.Errorf("invalid SSP tenant id: %w", err)
	}
	id, err := hex.DecodeString(tenant)
	if err != nil {
		return nil, fmt.Errorf("decode tenant id: %w", err)
	}
	return &SQLStore{db: db, tenantID: id, tenantHex: tenant}, nil
}

// TenantID returns the 32 character tenant id.
func (s *SQLStore) TenantID() string { return s.tenantHex }

// EnsureTenant creates the sandbox tenant row when it does not exist.
func (s *SQLStore) EnsureTenant(ctx context.Context, displayName string) error {
	if strings.TrimSpace(displayName) == "" {
		displayName = "sandbox"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant
		(tenant_id, display_name, status, default_currency, created_at, updated_at, version)
		VALUES (?, ?, 'active', 'USD', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6), 1)
		ON DUPLICATE KEY UPDATE display_name = VALUES(display_name)`, s.tenantID, displayName)
	return err
}

func (s *SQLStore) listPublishers(ctx context.Context) ([]Publisher, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT publisher_id, name, status, version, published_version
		FROM publisher WHERE tenant_id = ? ORDER BY name, publisher_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list publishers: %w", err)
	}
	defer rows.Close()
	items := make([]Publisher, 0)
	for rows.Next() {
		var id []byte
		var item Publisher
		if err := rows.Scan(&id, &item.Name, &item.Status, &item.Version, &item.PublishedVersion); err != nil {
			return nil, fmt.Errorf("scan publisher: %w", err)
		}
		item.ID = hex.EncodeToString(id)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) listProperties(ctx context.Context) ([]Property, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT property_id, publisher_id, name, property_type, external_id, status, version, published_version
		FROM property WHERE tenant_id = ? ORDER BY name, property_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list properties: %w", err)
	}
	defer rows.Close()
	items := make([]Property, 0)
	for rows.Next() {
		var id, publisherID []byte
		var item Property
		if err := rows.Scan(&id, &publisherID, &item.Name, &item.Type, &item.ExternalID, &item.Status, &item.Version, &item.PublishedVersion); err != nil {
			return nil, fmt.Errorf("scan property: %w", err)
		}
		item.ID, item.PublisherID = hex.EncodeToString(id), hex.EncodeToString(publisherID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) listAuctionPolicies(ctx context.Context) ([]AuctionPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT auction_policy_id, name, auction_type, tie_breaker, floor_enforcement, currency_policy, status, version, published_version
		FROM auction_policy WHERE tenant_id = ? ORDER BY name, auction_policy_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list auction policies: %w", err)
	}
	defer rows.Close()
	items := make([]AuctionPolicy, 0)
	for rows.Next() {
		var id []byte
		var item AuctionPolicy
		if err := rows.Scan(&id, &item.Name, &item.At, &item.TieBreaker, &item.FloorEnforcement, &item.CurrencyPolicy, &item.Status, &item.Version, &item.PublishedVersion); err != nil {
			return nil, fmt.Errorf("scan auction policy: %w", err)
		}
		item.ID = hex.EncodeToString(id)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) listPlacements(ctx context.Context) ([]Placement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT placement_id, property_id, auction_policy_id, name, media_type, width_px, height_px,
		floor_cpm_micros, currency, privacy_policy, allowed_origins, status, version, published_version
		FROM placement WHERE tenant_id = ? ORDER BY name, placement_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list placements: %w", err)
	}
	defer rows.Close()
	items := make([]Placement, 0)
	for rows.Next() {
		var id, propertyID, policyID []byte
		var origins sql.NullString
		var item Placement
		if err := rows.Scan(&id, &propertyID, &policyID, &item.Name, &item.MediaType, &item.Width, &item.Height,
			&item.FloorCPMMicros, &item.Currency, &item.PrivacyPolicy, &origins, &item.Status, &item.Version, &item.PublishedVersion); err != nil {
			return nil, fmt.Errorf("scan placement: %w", err)
		}
		item.ID, item.PropertyID, item.AuctionPolicyID = hex.EncodeToString(id), hex.EncodeToString(propertyID), hex.EncodeToString(policyID)
		item.AllowedOrigins = decodeOrigins(origins)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) listSupplyEndpoints(ctx context.Context) ([]SupplyEndpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT endpoint_id, property_id, name, protocol_version, credential_sha256,
		allowed_origins, rate_limit_per_second, status, version, published_version
		FROM supply_endpoint WHERE tenant_id = ? ORDER BY name, endpoint_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list supply endpoints: %w", err)
	}
	defer rows.Close()
	items := make([]SupplyEndpoint, 0)
	for rows.Next() {
		var id, propertyID []byte
		var credential []byte
		var origins sql.NullString
		var item SupplyEndpoint
		if err := rows.Scan(&id, &propertyID, &item.Name, &item.ProtocolVersion, &credential,
			&origins, &item.RateLimitPerSecond, &item.Status, &item.Version, &item.PublishedVersion); err != nil {
			return nil, fmt.Errorf("scan supply endpoint: %w", err)
		}
		item.ID, item.PropertyID = hex.EncodeToString(id), hex.EncodeToString(propertyID)
		item.CredentialConfigured = len(credential) == 32
		item.AllowedOrigins = decodeOrigins(origins)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) listDSPEndpoints(ctx context.Context) ([]DSPEndpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT dsp_endpoint_id, name, bid_url, openrtb_profile, callback_host_allowlist,
		priority, timeout_ms, status, version, published_version
		FROM dsp_endpoint WHERE tenant_id = ? ORDER BY priority, name, dsp_endpoint_id`, s.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list dsp endpoints: %w", err)
	}
	defer rows.Close()
	items := make([]DSPEndpoint, 0)
	for rows.Next() {
		var id []byte
		var callbacks []byte
		var item DSPEndpoint
		if err := rows.Scan(&id, &item.Name, &item.BidURL, &item.OpenRTBProfile, &callbacks,
			&item.Priority, &item.TimeoutMS, &item.Status, &item.Version, &item.PublishedVersion); err != nil {
			return nil, fmt.Errorf("scan dsp endpoint: %w", err)
		}
		item.ID = hex.EncodeToString(id)
		_ = json.Unmarshal(callbacks, &item.CallbackHosts)
		if item.CallbackHosts == nil {
			item.CallbackHosts = []string{}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListPublishers returns every publisher for the bound tenant.
func (s *SQLStore) ListPublishers(ctx context.Context) ([]Publisher, error) {
	return s.listPublishers(ctx)
}

// ListProperties returns every property for the bound tenant.
func (s *SQLStore) ListProperties(ctx context.Context) ([]Property, error) {
	return s.listProperties(ctx)
}

// ListAuctionPolicies returns every auction policy for the bound tenant.
func (s *SQLStore) ListAuctionPolicies(ctx context.Context) ([]AuctionPolicy, error) {
	return s.listAuctionPolicies(ctx)
}

// ListPlacements returns every placement for the bound tenant.
func (s *SQLStore) ListPlacements(ctx context.Context) ([]Placement, error) {
	return s.listPlacements(ctx)
}

// ListSupplyEndpoints returns every Supply profile for the bound tenant.
func (s *SQLStore) ListSupplyEndpoints(ctx context.Context) ([]SupplyEndpoint, error) {
	return s.listSupplyEndpoints(ctx)
}

// ListDSPEndpoints returns every bidder for the bound tenant.
func (s *SQLStore) ListDSPEndpoints(ctx context.Context) ([]DSPEndpoint, error) {
	return s.listDSPEndpoints(ctx)
}

func jsonCallbacks(hosts []string) ([]byte, error) {
	if hosts == nil {
		hosts = []string{}
	}
	return json.Marshal(hosts)
}

func decodeOrigins(value sql.NullString) []string {
	if !value.Valid || value.String == "" {
		return []string{}
	}
	var origins []string
	if err := json.Unmarshal([]byte(value.String), &origins); err != nil || origins == nil {
		return []string{}
	}
	return origins
}

func encodeOrigins(origins []string) []byte {
	if len(origins) == 0 {
		return nil
	}
	payload, _ := json.Marshal(origins)
	return payload
}

func decodeID(value string) ([]byte, error) {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if err := snapshot.ValidateID(value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	id, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return id, nil
}

// mapWriteError converts driver errors into stable API semantics.
func mapWriteError(err error) error {
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) {
		switch mysqlError.Number {
		case 1062:
			return &apiError{status: 409, code: CodeResourceConflict, message: "resource already exists"}
		case 1452:
			return ErrInvalidReference
		}
	}
	return err
}
