-- Migration: 000002_ssp_control_plane.up.sql
-- Description: Versioned SSP control plane, published projections and the DSP
-- creative whitelist consumed by the renderer.

ALTER TABLE publisher
  ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '' AFTER publisher_id,
  ADD COLUMN published_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version;

ALTER TABLE property
  ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '' AFTER publisher_id,
  ADD COLUMN published_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version;

ALTER TABLE auction_policy
  ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '' AFTER auction_policy_id,
  ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'draft' AFTER currency_policy,
  ADD COLUMN published_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version;

ALTER TABLE placement
  ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '' AFTER placement_id,
  ADD COLUMN width_px SMALLINT UNSIGNED NOT NULL DEFAULT 0 AFTER media_type,
  ADD COLUMN height_px SMALLINT UNSIGNED NOT NULL DEFAULT 0 AFTER width_px,
  ADD COLUMN allowed_origins JSON NULL AFTER privacy_policy,
  ADD COLUMN published_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version;

ALTER TABLE supply_endpoint
  ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '' AFTER endpoint_id,
  ADD COLUMN credential_sha256 BINARY(32) NULL AFTER protocol_version,
  ADD COLUMN allowed_origins JSON NULL AFTER credential_sha256,
  ADD COLUMN rate_limit_per_second INT UNSIGNED NOT NULL DEFAULT 0 AFTER allowed_origins,
  ADD COLUMN published_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version,
  ADD COLUMN updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) AFTER published_version;

ALTER TABLE dsp_endpoint
  ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '' AFTER dsp_endpoint_id,
  ADD COLUMN published_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER version;

ALTER TABLE ssp_config_outbox
  ADD COLUMN next_attempt_at DATETIME(6) NULL AFTER last_error_sanitized,
  ADD KEY idx_ssp_outbox_tenant_pending (tenant_id, published_at, next_attempt_at);

CREATE TABLE IF NOT EXISTS ssp_published_config (
  tenant_id BINARY(16) NOT NULL,
  aggregate_type VARCHAR(32) NOT NULL,
  aggregate_id BINARY(16) NOT NULL,
  config_version BIGINT UNSIGNED NOT NULL,
  payload JSON NOT NULL,
  published_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, aggregate_type, aggregate_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS ssp_config_manifest (
  tenant_id BINARY(16) NOT NULL,
  manifest_version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS dsp_endpoint_creative_whitelist (
  tenant_id BINARY(16) NOT NULL,
  dsp_endpoint_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NOT NULL,
  asset_ref VARCHAR(128) NOT NULL,
  asset_sha256 BINARY(32) NOT NULL,
  width_px SMALLINT UNSIGNED NOT NULL,
  height_px SMALLINT UNSIGNED NOT NULL,
  mime_type VARCHAR(32) NOT NULL,
  landing_url_host VARCHAR(255) NOT NULL,
  landing_url VARCHAR(2048) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  synced_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, dsp_endpoint_id, creative_id),
  KEY idx_dsp_whitelist_endpoint (tenant_id, dsp_endpoint_id, status)
) ENGINE=InnoDB;
