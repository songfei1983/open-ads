-- Migration: 000001_init_ssp_schema.up.sql
-- Description: Initialize SSP database schema and tables

CREATE TABLE IF NOT EXISTS tenant (
  tenant_id BINARY(16) NOT NULL,
  display_name VARCHAR(128) NOT NULL,
  status VARCHAR(16) NOT NULL,
  default_currency CHAR(3) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS publisher (
  tenant_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, publisher_id),
  CONSTRAINT fk_publisher_tenant FOREIGN KEY (tenant_id)
    REFERENCES tenant (tenant_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS property (
  tenant_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  property_type VARCHAR(16) NOT NULL,
  external_id VARCHAR(128) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, property_id),
  UNIQUE KEY uq_property_external (tenant_id, property_type, external_id),
  KEY idx_property_publisher (tenant_id, publisher_id, status),
  CONSTRAINT fk_property_publisher FOREIGN KEY (tenant_id, publisher_id)
    REFERENCES publisher (tenant_id, publisher_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS auction_policy (
  tenant_id BINARY(16) NOT NULL,
  auction_policy_id BINARY(16) NOT NULL,
  auction_type TINYINT UNSIGNED NOT NULL,
  tie_breaker VARCHAR(32) NOT NULL,
  floor_enforcement VARCHAR(16) NOT NULL,
  currency_policy VARCHAR(24) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, auction_policy_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS placement (
  tenant_id BINARY(16) NOT NULL,
  placement_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  auction_policy_id BINARY(16) NOT NULL,
  media_type VARCHAR(24) NOT NULL,
  floor_cpm_micros BIGINT SIGNED NOT NULL CHECK (floor_cpm_micros >= 0),
  currency CHAR(3) NOT NULL,
  privacy_policy VARCHAR(24) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, placement_id),
  KEY idx_placement_property (tenant_id, property_id, status),
  CONSTRAINT fk_placement_property FOREIGN KEY (tenant_id, property_id)
    REFERENCES property (tenant_id, property_id),
  CONSTRAINT fk_placement_auction_policy FOREIGN KEY (tenant_id, auction_policy_id)
    REFERENCES auction_policy (tenant_id, auction_policy_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS supply_endpoint (
  tenant_id BINARY(16) NOT NULL,
  endpoint_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  protocol_version VARCHAR(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, endpoint_id),
  KEY idx_endpoint_property (tenant_id, property_id, status),
  CONSTRAINT fk_endpoint_property FOREIGN KEY (tenant_id, property_id)
    REFERENCES property (tenant_id, property_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS dsp_endpoint (
  tenant_id BINARY(16) NOT NULL,
  dsp_endpoint_id BINARY(16) NOT NULL,
  bid_url VARCHAR(2048) NOT NULL,
  openrtb_profile VARCHAR(32) NOT NULL,
  callback_host_allowlist JSON NOT NULL,
  priority INT SIGNED NOT NULL DEFAULT 0,
  timeout_ms INT UNSIGNED NOT NULL,
  status VARCHAR(16) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, dsp_endpoint_id),
  KEY idx_dsp_endpoint_status_priority (tenant_id, status, priority)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS ssp_config_outbox (
  event_id BINARY(16) NOT NULL,
  tenant_id BINARY(16) NOT NULL,
  aggregate_type VARCHAR(32) NOT NULL,
  aggregate_id BINARY(16) NOT NULL,
  aggregate_version BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(6) NOT NULL,
  published_at DATETIME(6) NULL,
  failed_attempts SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  last_failed_at DATETIME(6) NULL,
  last_error_sanitized VARCHAR(512) NULL,
  PRIMARY KEY (event_id),
  KEY idx_ssp_outbox_pending (published_at, created_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS publisher_subject (
  tenant_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  subject_id BINARY(16) NOT NULL,
  source_namespace VARCHAR(128) NULL,
  source_subject_hmac BINARY(32) NULL,
  hmac_key_version SMALLINT UNSIGNED NULL,
  status VARCHAR(16) NOT NULL,
  consent_version VARCHAR(64) NOT NULL,
  consent_purpose_mask BIGINT UNSIGNED NOT NULL,
  issued_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  revoked_at DATETIME(6) NULL,
  PRIMARY KEY (tenant_id, publisher_id, subject_id),
  UNIQUE KEY uq_publisher_subject_source (tenant_id, publisher_id, source_namespace, source_subject_hmac),
  KEY idx_subject_expiry (tenant_id, expires_at),
  CONSTRAINT fk_subject_publisher FOREIGN KEY (tenant_id, publisher_id)
    REFERENCES publisher (tenant_id, publisher_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS ssp_identity_revocation (
  tenant_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  subject_id BINARY(16) NOT NULL,
  revocation_id BINARY(16) NOT NULL,
  gate_generation BIGINT UNSIGNED NOT NULL,
  status VARCHAR(16) NOT NULL,
  requested_at DATETIME(6) NOT NULL,
  completed_at DATETIME(6) NULL,
  PRIMARY KEY (tenant_id, revocation_id),
  UNIQUE KEY uq_subject_revocation_generation (tenant_id, publisher_id, subject_id, gate_generation),
  CONSTRAINT fk_revocation_subject FOREIGN KEY (tenant_id, publisher_id, subject_id)
    REFERENCES publisher_subject (tenant_id, publisher_id, subject_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS ssp_delivery_event (
  tenant_id BINARY(16) NOT NULL,
  event_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  event_type VARCHAR(24) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  bid_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  property_id BINARY(16) NOT NULL,
  placement_id BINARY(16) NOT NULL,
  dsp_id BINARY(16) NULL,
  creative_id BINARY(16) NULL,
  currency CHAR(3) NULL,
  amount_micros BIGINT SIGNED NULL CHECK (amount_micros IS NULL OR amount_micros >= 0),
  occurred_at DATETIME(6) NOT NULL,
  received_at DATETIME(6) NOT NULL,
  schema_version SMALLINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, event_id),
  UNIQUE KEY uq_ssp_event_source (tenant_id, source_event_key),
  KEY idx_ssp_event_auction (tenant_id, auction_id, bid_id, imp_id, occurred_at),
  KEY idx_ssp_event_publisher_time (tenant_id, publisher_id, occurred_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS ssp_billable_outbox (
  tenant_id BINARY(16) NOT NULL,
  event_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  dsp_id BINARY(16) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  bid_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  reservation_ref VARBINARY(2048) NOT NULL,
  burl_target_url VARCHAR(2048) NOT NULL,
  amount_micros BIGINT SIGNED NOT NULL CHECK (amount_micros >= 0),
  currency CHAR(3) NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  attempt_count INT UNSIGNED NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(6) NOT NULL,
  status VARCHAR(16) NOT NULL CHECK (status IN ('pending', 'sending', 'delivered', 'quarantined')),
  published_at DATETIME(6) NULL,
  last_http_status SMALLINT UNSIGNED NULL,
  last_error_sanitized VARCHAR(512) NULL,
  PRIMARY KEY (tenant_id, event_id),
  UNIQUE KEY uq_ssp_billable_outbox_source (tenant_id, source_event_key),
  KEY idx_ssp_billable_outbox_pending (published_at, next_attempt_at),
  CONSTRAINT fk_ssp_billable_outbox_event FOREIGN KEY (tenant_id, event_id)
    REFERENCES ssp_delivery_event (tenant_id, event_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS publisher_payable_ledger (
  tenant_id BINARY(16) NOT NULL,
  entry_id BINARY(16) NOT NULL,
  idempotency_key BINARY(32) NOT NULL,
  source_event_id BINARY(16) NOT NULL,
  publisher_id BINARY(16) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  entry_type VARCHAR(16) NOT NULL,
  amount_micros BIGINT SIGNED NOT NULL CHECK (amount_micros <> 0),
  reverses_entry_id BINARY(16) NULL,
  currency CHAR(3) NOT NULL,
  publisher_policy_version BIGINT UNSIGNED NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, entry_id),
  UNIQUE KEY uq_payable_ledger_idempotency (tenant_id, idempotency_key),
  KEY idx_payable_publisher_time (tenant_id, publisher_id, occurred_at)
) ENGINE=InnoDB;
