-- Migration: 000002_ssp_control_plane.down.sql
-- Description: Roll back the SSP control plane additions.

DROP TABLE IF EXISTS dsp_endpoint_creative_whitelist;
DROP TABLE IF EXISTS ssp_config_manifest;
DROP TABLE IF EXISTS ssp_published_config;

ALTER TABLE ssp_config_outbox
  DROP KEY idx_ssp_outbox_tenant_pending,
  DROP COLUMN next_attempt_at;

ALTER TABLE dsp_endpoint
  DROP COLUMN published_version,
  DROP COLUMN name;

ALTER TABLE supply_endpoint
  DROP COLUMN updated_at,
  DROP COLUMN published_version,
  DROP COLUMN rate_limit_per_second,
  DROP COLUMN allowed_origins,
  DROP COLUMN credential_sha256,
  DROP COLUMN name;

ALTER TABLE placement
  DROP COLUMN published_version,
  DROP COLUMN allowed_origins,
  DROP COLUMN height_px,
  DROP COLUMN width_px,
  DROP COLUMN name;

ALTER TABLE auction_policy
  DROP COLUMN published_version,
  DROP COLUMN status,
  DROP COLUMN name;

ALTER TABLE property
  DROP COLUMN published_version,
  DROP COLUMN name;

ALTER TABLE publisher
  DROP COLUMN published_version,
  DROP COLUMN name;
