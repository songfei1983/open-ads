-- Migration: 000003_ssp_ledger.down.sql
-- Description: Revert the ledger invariants and billable outbox resolution state.

ALTER TABLE ssp_billable_outbox DROP CONSTRAINT chk_ssp_billable_outbox_status;
ALTER TABLE ssp_billable_outbox
  DROP COLUMN resolved_at,
  DROP COLUMN resolution_reason;
ALTER TABLE ssp_billable_outbox MODIFY COLUMN status VARCHAR(16) NOT NULL
  CHECK (status IN ('pending', 'sending', 'delivered', 'quarantined'));

ALTER TABLE publisher_payable_ledger
  DROP FOREIGN KEY fk_publisher_payable_event,
  DROP FOREIGN KEY fk_publisher_payable_reversal,
  DROP KEY idx_publisher_payable_reversal,
  DROP CONSTRAINT chk_publisher_payable_reversal,
  DROP CONSTRAINT chk_publisher_payable_type;
