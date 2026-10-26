package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/valkey"
)

const publishManifestScript = `
local current = redis.call('GET', KEYS[1])
if current then
  local current_version = string.sub(current, 1, 32)
  if current_version >= ARGV[1] then return 0 end
end
redis.call('SET', KEYS[1], ARGV[1] .. '\n' .. ARGV[2])
return 1
`

// PublishPending atomically publishes the latest full tenant projection to
// Valkey and acknowledges a bounded batch of outbox events only on success.
func (s *SQLStore) PublishPending(ctx context.Context, client *valkey.Client) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, fmt.Errorf("begin DSP outbox publish: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT event_id FROM dsp_config_outbox
		WHERE tenant_id = ? AND published_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at <= UTC_TIMESTAMP(6))
		ORDER BY created_at LIMIT 64 FOR UPDATE SKIP LOCKED`, s.tenantID)
	if err != nil {
		return false, fmt.Errorf("claim DSP config outbox: %w", err)
	}
	var eventIDs [][]byte
	for rows.Next() {
		var eventID []byte
		if err := rows.Scan(&eventID); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan DSP config outbox: %w", err)
		}
		eventIDs = append(eventIDs, append([]byte(nil), eventID...))
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return false, fmt.Errorf("iterate DSP config outbox: %w", rowErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close DSP config outbox rows: %w", closeErr)
	}
	manifest, err := s.loadPublishedManifest(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		s.recordPublishFailure(ctx, eventIDs)
		return false, err
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("encode DSP manifest: %w", err)
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
		return false, fmt.Errorf("encode DSP manifest envelope: %w", err)
	}
	tenant := hex.EncodeToString(s.tenantID)
	key := "dsp:manifest:{" + tenant + "}"
	result, err := client.Do(ctx, "EVAL", publishManifestScript, "1", key, manifest.Version, string(payload))
	if err != nil {
		_ = tx.Rollback()
		s.recordPublishFailure(ctx, eventIDs)
		return false, fmt.Errorf("publish DSP manifest to Valkey: %w", err)
	}
	if _, ok := result.(int64); !ok {
		_ = tx.Rollback()
		s.recordPublishFailure(ctx, eventIDs)
		return false, fmt.Errorf("unexpected Valkey manifest publish response %T", result)
	}
	if len(eventIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit DSP manifest reconciliation: %w", err)
		}
		return true, nil
	}
	for _, eventID := range eventIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE dsp_config_outbox SET published_at = ?, last_error_sanitized = NULL, next_attempt_at = NULL
			WHERE tenant_id = ? AND event_id = ? AND published_at IS NULL`, time.Now().UTC(), s.tenantID, eventID); err != nil {
			return false, fmt.Errorf("acknowledge DSP config outbox: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit DSP outbox publish: %w", err)
	}
	return true, nil
}

func (s *SQLStore) recordPublishFailure(ctx context.Context, eventIDs [][]byte) {
	for _, eventID := range eventIDs {
		_, _ = s.db.ExecContext(ctx, `UPDATE dsp_config_outbox
			SET last_failed_at = ?,
			last_error_sanitized = 'valkey_publish_failed',
			next_attempt_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL LEAST(300, POW(2, LEAST(failed_attempts + 1, 8))) SECOND),
			failed_attempts = LEAST(65535, failed_attempts + 1)
			WHERE tenant_id = ? AND event_id = ? AND published_at IS NULL`, time.Now().UTC(), s.tenantID, eventID)
	}
}
