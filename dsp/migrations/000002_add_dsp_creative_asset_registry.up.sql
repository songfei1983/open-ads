-- Migration: 000002_add_dsp_creative_asset_registry.up.sql
-- Description: Record server-verified creative asset metadata.

CREATE TABLE dsp_creative_asset_registry (
  tenant_id BINARY(16) NOT NULL,
  asset_ref VARCHAR(128) NOT NULL,
  asset_sha256 BINARY(32) NOT NULL,
  content_size_bytes BIGINT UNSIGNED NOT NULL,
  width_px SMALLINT UNSIGNED NOT NULL,
  height_px SMALLINT UNSIGNED NOT NULL,
  mime_type VARCHAR(32) NOT NULL,
  uploaded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, asset_ref)
) ENGINE=InnoDB;
