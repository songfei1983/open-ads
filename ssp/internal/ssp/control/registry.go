package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *SQLStore) loadWhitelist(ctx context.Context, q queryer, endpointID []byte) (map[string]snapshot.Creative, error) {
	rows, err := q.QueryContext(ctx, `SELECT creative_id, asset_ref, asset_sha256, width_px, height_px,
		mime_type, landing_url_host, landing_url, status, version
		FROM dsp_endpoint_creative_whitelist WHERE tenant_id = ? AND dsp_endpoint_id = ?`, s.tenantID, endpointID)
	if err != nil {
		return nil, fmt.Errorf("load creative whitelist: %w", err)
	}
	defer rows.Close()
	creatives := make(map[string]snapshot.Creative)
	for rows.Next() {
		var id, hash []byte
		var creative snapshot.Creative
		if err := rows.Scan(&id, &creative.AssetRef, &hash, &creative.Width, &creative.Height,
			&creative.MIMEType, &creative.LandingURLHost, &creative.LandingURL, &creative.Status, &creative.Version); err != nil {
			return nil, fmt.Errorf("scan creative whitelist: %w", err)
		}
		creative.ID = hex.EncodeToString(id)
		creative.AssetSHA256 = hex.EncodeToString(hash)
		creatives[creative.ID] = creative
	}
	return creatives, rows.Err()
}

// ListCreativeWhitelist returns the read-only whitelist for an endpoint.
func (s *SQLStore) ListCreativeWhitelist(ctx context.Context, endpointID string) (CreativeWhitelistResponse, error) {
	idBytes, err := decodeID(endpointID)
	if err != nil {
		return CreativeWhitelistResponse{}, err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM dsp_endpoint WHERE tenant_id = ? AND dsp_endpoint_id = ?`,
		s.tenantID, idBytes).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return CreativeWhitelistResponse{}, ErrNotFound
	} else if err != nil {
		return CreativeWhitelistResponse{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT creative_id, asset_ref, asset_sha256, width_px, height_px,
		mime_type, landing_url_host, status, version
		FROM dsp_endpoint_creative_whitelist WHERE tenant_id = ? AND dsp_endpoint_id = ?
		ORDER BY creative_id`, s.tenantID, idBytes)
	if err != nil {
		return CreativeWhitelistResponse{}, fmt.Errorf("list creative whitelist: %w", err)
	}
	defer rows.Close()
	response := CreativeWhitelistResponse{DSPEndpointID: endpointID, Whitelist: []CreativeWhitelistEntry{}}
	for rows.Next() {
		var id, hash []byte
		var entry CreativeWhitelistEntry
		if err := rows.Scan(&id, &entry.AssetRef, &hash, &entry.Width, &entry.Height,
			&entry.MIMEType, &entry.LandingURLHost, &entry.Status, &entry.Version); err != nil {
			return CreativeWhitelistResponse{}, fmt.Errorf("scan creative whitelist: %w", err)
		}
		entry.CreativeID = hex.EncodeToString(id)
		entry.AssetSHA256 = hex.EncodeToString(hash)
		response.Whitelist = append(response.Whitelist, entry)
	}
	return response, rows.Err()
}

// UpsertCreativeRegistry is the only write path into the SSP whitelist. It
// bumps the endpoint version, refreshes the immutable endpoint projection and
// records an outbox event so every instance reloads the same snapshot.
func (s *SQLStore) UpsertCreativeRegistry(ctx context.Context, endpointID string, entries []CreativeRegistration) (int, error) {
	idBytes, err := decodeID(endpointID)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin creative registry upsert: %w", err)
	}
	defer tx.Rollback()

	var currentVersion, publishedVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT version, published_version FROM dsp_endpoint
		WHERE tenant_id = ? AND dsp_endpoint_id = ? FOR UPDATE`, s.tenantID, idBytes).
		Scan(&currentVersion, &publishedVersion); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("lock dsp endpoint: %w", err)
	}
	if publishedVersion == 0 {
		return 0, ErrRegistryNotReady
	}
	accepted := 0
	for _, entry := range entries {
		creativeID, err := decodeID(entry.CreativeID)
		if err != nil {
			return 0, err
		}
		hash, err := hex.DecodeString(entry.AssetSHA256)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO dsp_endpoint_creative_whitelist
			(tenant_id, dsp_endpoint_id, creative_id, asset_ref, asset_sha256, width_px, height_px,
			 mime_type, landing_url_host, landing_url, status, version, synced_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(6))
			ON DUPLICATE KEY UPDATE asset_ref = VALUES(asset_ref), asset_sha256 = VALUES(asset_sha256),
			width_px = VALUES(width_px), height_px = VALUES(height_px), mime_type = VALUES(mime_type),
			landing_url_host = VALUES(landing_url_host), landing_url = VALUES(landing_url),
			status = VALUES(status), version = GREATEST(version, VALUES(version)), synced_at = UTC_TIMESTAMP(6)`,
			s.tenantID, idBytes, creativeID, entry.AssetRef, hash, entry.Width, entry.Height,
			entry.MIMEType, entry.LandingURLHost, entry.LandingURL, entry.Status, entry.Version); err != nil {
			return 0, mapWriteError(err)
		}
		accepted++
	}
	newVersion := currentVersion + 1
	if _, err := tx.ExecContext(ctx, `UPDATE dsp_endpoint SET version = ?, published_version = ?,
		updated_at = UTC_TIMESTAMP(6) WHERE tenant_id = ? AND dsp_endpoint_id = ?`,
		newVersion, newVersion, s.tenantID, idBytes); err != nil {
		return 0, mapWriteError(err)
	}
	payload, err := s.buildPublishedPayload(ctx, tx, AggregateDSPEndpoint, idBytes, newVersion)
	if err != nil {
		return 0, err
	}
	if err := s.writePublishedConfig(ctx, tx, AggregateDSPEndpoint, idBytes, newVersion, payload); err != nil {
		return 0, err
	}
	if err := s.writeOutbox(ctx, tx, AggregateDSPEndpoint, idBytes, newVersion); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit creative registry upsert: %w", err)
	}
	return accepted, nil
}
