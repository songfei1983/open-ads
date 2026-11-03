-- Migration: 000006_dsp_spend_audit.up.sql
-- Description: Immutable audit trail for advertiser spend adjustments.

CREATE TABLE IF NOT EXISTS advertiser_spend_audit (
  tenant_id BINARY(16) NOT NULL,
  audit_id BINARY(16) NOT NULL,
  entry_id BINARY(16) NOT NULL,
  reverses_entry_id BINARY(16) NULL,
  action VARCHAR(24) NOT NULL,
  amount_micros BIGINT SIGNED NOT NULL,
  reason VARCHAR(512) NOT NULL,
  actor VARCHAR(64) NOT NULL,
  request_id VARCHAR(64) NULL,
  recorded_at DATETIME(6) NOT NULL,
  PRIMARY KEY (tenant_id, audit_id),
  KEY idx_spend_audit_entry (tenant_id, entry_id, recorded_at),
  CONSTRAINT chk_spend_audit_action CHECK (action IN ('reversal')),
  CONSTRAINT fk_spend_audit_entry FOREIGN KEY (tenant_id, entry_id)
    REFERENCES advertiser_spend_ledger (tenant_id, entry_id)
) ENGINE=InnoDB;
