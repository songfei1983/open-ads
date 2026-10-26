-- Keep one currently published projection per resource. Draft edits never alter this table.
CREATE TABLE dsp_config_manifest_clock (
  tenant_id BINARY(16) NOT NULL,
  manifest_version BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id)
) ENGINE=InnoDB;

CREATE TABLE dsp_published_config (
  tenant_id BINARY(16) NOT NULL,
  aggregate_type VARCHAR(32) NOT NULL,
  aggregate_id BINARY(16) NOT NULL,
  aggregate_version BIGINT UNSIGNED NOT NULL,
  config_version BIGINT UNSIGNED NOT NULL,
  payload JSON NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, aggregate_type, aggregate_id),
  KEY idx_dsp_published_config_version (tenant_id, config_version)
) ENGINE=InnoDB;

ALTER TABLE dsp_config_outbox
  ADD COLUMN next_attempt_at DATETIME(6) NULL,
  ADD KEY idx_dsp_outbox_retry (tenant_id, published_at, next_attempt_at, created_at);
