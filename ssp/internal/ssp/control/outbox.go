package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/snapshot"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/valkey"
)

const publishManifestScript = `
local current = redis.call('GET', KEYS[1])
if current then
  local current_version = string.sub(current, 1, 20)
  if current_version >= ARGV[1] then return 0 end
end
redis.call('SET', KEYS[1], ARGV[1] .. '\n' .. ARGV[2])
return 1
`

// PublishPending writes the current projection to Valkey and acknowledges the
// claimed outbox events only after the manifest is durable.
func (s *SQLStore) PublishPending(ctx context.Context, client *valkey.Client) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, fmt.Errorf("begin SSP outbox publish: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT event_id FROM ssp_config_outbox
		WHERE tenant_id = ? AND published_at IS NULL
		AND (next_attempt_at IS NULL OR next_attempt_at <= UTC_TIMESTAMP(6))
		ORDER BY created_at LIMIT 64 FOR UPDATE SKIP LOCKED`, s.tenantID)
	if err != nil {
		return false, fmt.Errorf("claim SSP config outbox: %w", err)
	}
	var eventIDs [][]byte
	for rows.Next() {
		var eventID []byte
		if err := rows.Scan(&eventID); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan SSP config outbox: %w", err)
		}
		eventIDs = append(eventIDs, append([]byte(nil), eventID...))
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return false, fmt.Errorf("iterate SSP config outbox: %w", rowErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close SSP config outbox rows: %w", closeErr)
	}

	manifestVersion, err := s.claimManifestVersion(ctx, tx, len(eventIDs) > 0)
	if err != nil {
		return false, err
	}
	manifest, err := s.loadPublishedManifest(ctx, tx, manifestVersion)
	if errors.Is(err, ErrNoPublishedConfig) {
		// Nothing has been published yet; keep the outbox untouched.
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit empty SSP outbox publish: %w", commitErr)
		}
		return false, nil
	}
	if err != nil {
		_ = tx.Rollback()
		s.recordPublishFailure(ctx, eventIDs)
		return false, err
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("encode SSP manifest: %w", err)
	}
	manifestHash := sha256.Sum256(manifestPayload)
	payload, err := json.Marshal(struct {
		SchemaVersion int             `json:"schema_version"`
		Version       string          `json:"version"`
		GeneratedAt   time.Time       `json:"generated_at"`
		PayloadHash   string          `json:"payload_hash"`
		Manifest      json.RawMessage `json:"manifest"`
	}{1, manifest.Version, time.Now().UTC(), hex.EncodeToString(manifestHash[:]), manifestPayload})
	if err != nil {
		return false, fmt.Errorf("encode SSP manifest envelope: %w", err)
	}
	key := "ssp:manifest:{" + s.tenantHex + "}"
	result, err := client.Do(ctx, "EVAL", publishManifestScript, "1", key, manifest.Version, string(payload))
	if err != nil {
		_ = tx.Rollback()
		s.recordPublishFailure(ctx, eventIDs)
		return false, fmt.Errorf("publish SSP manifest to Valkey: %w", err)
	}
	if _, ok := result.(int64); !ok {
		_ = tx.Rollback()
		s.recordPublishFailure(ctx, eventIDs)
		return false, fmt.Errorf("unexpected Valkey manifest publish response %T", result)
	}
	if len(eventIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit SSP manifest reconciliation: %w", err)
		}
		return true, nil
	}
	for _, eventID := range eventIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE ssp_config_outbox
			SET published_at = UTC_TIMESTAMP(6), last_error_sanitized = NULL, next_attempt_at = NULL
			WHERE tenant_id = ? AND event_id = ? AND published_at IS NULL`, s.tenantID, eventID); err != nil {
			return false, fmt.Errorf("acknowledge SSP config outbox: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit SSP outbox publish: %w", err)
	}
	return true, nil
}

func (s *SQLStore) claimManifestVersion(ctx context.Context, tx *sql.Tx, increment bool) (string, error) {
	var version uint64
	err := tx.QueryRowContext(ctx, `SELECT manifest_version FROM ssp_config_manifest
		WHERE tenant_id = ? FOR UPDATE`, s.tenantID).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		version = 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO ssp_config_manifest (tenant_id, manifest_version, updated_at)
			VALUES (?, ?, UTC_TIMESTAMP(6))`, s.tenantID, version); err != nil {
			return "", fmt.Errorf("initialize SSP manifest version: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("lock SSP manifest version: %w", err)
	default:
		if increment {
			version++
			if _, err := tx.ExecContext(ctx, `UPDATE ssp_config_manifest SET manifest_version = ?, updated_at = UTC_TIMESTAMP(6)
				WHERE tenant_id = ?`, version, s.tenantID); err != nil {
				return "", fmt.Errorf("advance SSP manifest version: %w", err)
			}
		}
	}
	return snapshot.FormatVersion(version), nil
}

func (s *SQLStore) recordPublishFailure(ctx context.Context, eventIDs [][]byte) {
	for _, eventID := range eventIDs {
		_, _ = s.db.ExecContext(ctx, `UPDATE ssp_config_outbox
			SET last_failed_at = UTC_TIMESTAMP(6),
			last_error_sanitized = 'valkey_publish_failed',
			next_attempt_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL LEAST(300, POW(2, LEAST(failed_attempts + 1, 8))) SECOND),
			failed_attempts = LEAST(65535, failed_attempts + 1)
			WHERE tenant_id = ? AND event_id = ? AND published_at IS NULL`, s.tenantID, eventID)
	}
}

// ListOutbox returns pending or failed configuration publishes.
func (s *SQLStore) ListOutbox(ctx context.Context, status string, limit int) ([]OutboxEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "published_at IS NULL"
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "pending":
		where += " AND failed_attempts < 5"
	case "failed":
		where += " AND failed_attempts >= 5"
	default:
		return nil, &apiError{status: 422, code: CodeBusinessRule, message: "status must be pending or failed"}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, aggregate_type, aggregate_id, aggregate_version,
		created_at, failed_attempts, last_failed_at, last_error_sanitized
		FROM ssp_config_outbox WHERE tenant_id = ? AND `+where+`
		ORDER BY created_at DESC LIMIT ?`, s.tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list SSP config outbox: %w", err)
	}
	defer rows.Close()
	events := make([]OutboxEvent, 0)
	for rows.Next() {
		var id, aggregateID []byte
		var createdAt time.Time
		var lastFailedAt sql.NullTime
		var lastError sql.NullString
		var event OutboxEvent
		if err := rows.Scan(&id, &event.AggregateType, &aggregateID, &event.AggregateVersion,
			&createdAt, &event.FailedAttempts, &lastFailedAt, &lastError); err != nil {
			return nil, fmt.Errorf("scan SSP config outbox: %w", err)
		}
		event.EventID = hex.EncodeToString(id)
		event.AggregateID = hex.EncodeToString(aggregateID)
		event.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		if lastFailedAt.Valid {
			event.LastFailedAt = lastFailedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if lastError.Valid {
			event.LastErrorSanitized = lastError.String
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// RetryOutbox resets the failure counters so the publisher claims the event.
func (s *SQLStore) RetryOutbox(ctx context.Context, eventID string) error {
	idBytes, err := decodeID(eventID)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ssp_config_outbox
		SET failed_attempts = 0, last_failed_at = NULL, last_error_sanitized = NULL, next_attempt_at = NULL
		WHERE tenant_id = ? AND event_id = ? AND published_at IS NULL`, s.tenantID, idBytes)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
