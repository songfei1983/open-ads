-- Migration: 000001_init_dsp_schema.up.sql
-- Description: Initialize DSP database schema and tables

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

CREATE TABLE IF NOT EXISTS dsp_config_outbox (
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
  KEY idx_dsp_outbox_pending (published_at, created_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS advertiser (
  tenant_id BINARY(16) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  default_currency CHAR(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, advertiser_id),
  CONSTRAINT fk_advertiser_tenant FOREIGN KEY (tenant_id)
    REFERENCES tenant (tenant_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS campaign (
  tenant_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  starts_at DATETIME(6) NOT NULL,
  ends_at DATETIME(6) NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, campaign_id),
  KEY idx_campaign_advertiser (tenant_id, advertiser_id, status),
  CONSTRAINT fk_campaign_advertiser FOREIGN KEY (tenant_id, advertiser_id)
    REFERENCES advertiser (tenant_id, advertiser_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS line_item (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  starts_at DATETIME(6) NULL,
  ends_at DATETIME(6) NULL,
  currency CHAR(3) NOT NULL,
  bid_cpm_micros BIGINT SIGNED NOT NULL CHECK (bid_cpm_micros >= 0),
  daily_budget_micros BIGINT SIGNED NOT NULL CHECK (daily_budget_micros >= 0),
  lifetime_budget_micros BIGINT SIGNED NOT NULL CHECK (lifetime_budget_micros >= 0),
  targeting_version BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id),
  KEY idx_line_campaign (tenant_id, campaign_id, status),
  CONSTRAINT fk_line_campaign FOREIGN KEY (tenant_id, campaign_id)
    REFERENCES campaign (tenant_id, campaign_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS line_item_targeting_version (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  targeting_version BIGINT UNSIGNED NOT NULL,
  targeting_spec JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id, targeting_version),
  CONSTRAINT fk_targeting_line FOREIGN KEY (tenant_id, line_item_id)
    REFERENCES line_item (tenant_id, line_item_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS creative (
  tenant_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NOT NULL,
  status VARCHAR(16) NOT NULL,
  format VARCHAR(24) NOT NULL,
  asset_ref VARCHAR(128) NOT NULL,
  asset_sha256 BINARY(32) NOT NULL,
  asset_width_px SMALLINT UNSIGNED NOT NULL,
  asset_height_px SMALLINT UNSIGNED NOT NULL,
  landing_url VARCHAR(2048) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, creative_id),
  KEY idx_creative_format (tenant_id, format, status)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS line_item_creative (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id, creative_id),
  CONSTRAINT fk_lic_line FOREIGN KEY (tenant_id, line_item_id)
    REFERENCES line_item (tenant_id, line_item_id),
  CONSTRAINT fk_lic_creative FOREIGN KEY (tenant_id, creative_id)
    REFERENCES creative (tenant_id, creative_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS budget_account (
  tenant_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  budget_scope VARCHAR(16) NOT NULL,
  period_start DATE NOT NULL,
  currency CHAR(3) NOT NULL,
  budget_limit_micros BIGINT SIGNED NOT NULL CHECK (budget_limit_micros >= 0),
  spent_micros BIGINT SIGNED NOT NULL CHECK (spent_micros >= 0),
  leased_micros BIGINT SIGNED NOT NULL CHECK (leased_micros >= 0),
  version BIGINT UNSIGNED NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, line_item_id, budget_scope, period_start),
  CONSTRAINT fk_budget_line FOREIGN KEY (tenant_id, line_item_id)
    REFERENCES line_item (tenant_id, line_item_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS budget_lease (
  tenant_id BINARY(16) NOT NULL,
  lease_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  budget_scope VARCHAR(16) NOT NULL,
  period_start DATE NOT NULL,
  owner_instance VARCHAR(128) NOT NULL,
  amount_micros BIGINT SIGNED NOT NULL CHECK (amount_micros >= 0),
  held_micros BIGINT SIGNED NOT NULL CHECK (held_micros >= 0),
  settlement_grace_until DATETIME(6) NOT NULL,
  status VARCHAR(16) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, lease_id),
  KEY idx_lease_expiry (status, expires_at, settlement_grace_until),
  KEY idx_lease_owner (tenant_id, owner_instance, line_item_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS budget_reservation_settlement (
  tenant_id BINARY(16) NOT NULL,
  reservation_id BINARY(16) NOT NULL,
  budget_scope VARCHAR(16) NOT NULL,
  period_start DATE NOT NULL,
  lease_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  outcome VARCHAR(16) NOT NULL,
  reserved_max_micros BIGINT SIGNED NOT NULL CHECK (reserved_max_micros >= 0),
  actual_amount_micros BIGINT SIGNED NOT NULL CHECK (actual_amount_micros >= 0),
  currency CHAR(3) NOT NULL,
  settled_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, reservation_id, budget_scope, period_start),
  UNIQUE KEY uq_reservation_source_event (tenant_id, source_event_key, budget_scope, period_start),
  CONSTRAINT fk_reservation_lease FOREIGN KEY (tenant_id, lease_id)
    REFERENCES budget_lease (tenant_id, lease_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS dsp_notice_quarantine (
  tenant_id BINARY(16) NOT NULL,
  quarantine_id BINARY(16) NOT NULL,
  reservation_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  outcome VARCHAR(16) NOT NULL,
  reason_code VARCHAR(48) NOT NULL,
  payload_hash BINARY(32) NOT NULL,
  status VARCHAR(16) NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected')),
  received_at DATETIME(6) NOT NULL,
  resolved_at DATETIME(6) NULL,
  resolved_by BINARY(16) NULL,
  resolution_reason VARCHAR(512) NULL,
  PRIMARY KEY (tenant_id, quarantine_id),
  UNIQUE KEY uq_dsp_quarantine_event (tenant_id, source_event_key),
  KEY idx_dsp_quarantine_status (tenant_id, status, received_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS dsp_delivery_event (
  tenant_id BINARY(16) NOT NULL,
  event_id BINARY(16) NOT NULL,
  source_event_key BINARY(32) NOT NULL,
  event_type VARCHAR(24) NOT NULL,
  auction_id VARCHAR(128) NOT NULL,
  bid_id VARCHAR(128) NOT NULL,
  imp_id VARCHAR(128) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  creative_id BINARY(16) NULL,
  currency CHAR(3) NOT NULL,
  amount_micros BIGINT SIGNED NULL CHECK (amount_micros IS NULL OR amount_micros >= 0),
  occurred_at DATETIME(6) NOT NULL,
  received_at DATETIME(6) NOT NULL,
  schema_version SMALLINT UNSIGNED NOT NULL,
  PRIMARY KEY (tenant_id, event_id),
  UNIQUE KEY uq_dsp_event_source (tenant_id, source_event_key),
  KEY idx_dsp_event_auction (tenant_id, auction_id, imp_id, occurred_at),
  KEY idx_dsp_event_line_time (tenant_id, line_item_id, occurred_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS advertiser_spend_ledger (
  tenant_id BINARY(16) NOT NULL,
  entry_id BINARY(16) NOT NULL,
  idempotency_key BINARY(32) NOT NULL,
  source_event_id BINARY(16) NOT NULL,
  advertiser_id BINARY(16) NOT NULL,
  campaign_id BINARY(16) NOT NULL,
  line_item_id BINARY(16) NOT NULL,
  entry_type VARCHAR(16) NOT NULL,
  amount_micros BIGINT SIGNED NOT NULL CHECK (amount_micros <> 0),
  reverses_entry_id BINARY(16) NULL,
  currency CHAR(3) NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, entry_id),
  UNIQUE KEY uq_spend_ledger_idempotency (tenant_id, idempotency_key),
  KEY idx_spend_advertiser_time (tenant_id, advertiser_id, occurred_at)
) ENGINE=InnoDB;
