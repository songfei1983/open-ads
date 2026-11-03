-- Migration: 000003_ssp_ledger.up.sql
-- Description: Enforce append-only ledger invariants and extend the billable
--              outbox with operator resolution state.

ALTER TABLE publisher_payable_ledger
  ADD CONSTRAINT chk_publisher_payable_type CHECK (entry_type IN ('billable', 'reversal', 'adjustment')),
  ADD CONSTRAINT chk_publisher_payable_reversal CHECK ((entry_type = 'reversal') = (reverses_entry_id IS NOT NULL)),
  ADD KEY idx_publisher_payable_reversal (tenant_id, reverses_entry_id),
  ADD CONSTRAINT fk_publisher_payable_reversal FOREIGN KEY (tenant_id, reverses_entry_id)
    REFERENCES publisher_payable_ledger (tenant_id, entry_id),
  ADD CONSTRAINT fk_publisher_payable_event FOREIGN KEY (tenant_id, source_event_id)
    REFERENCES ssp_delivery_event (tenant_id, event_id);

-- The status CHECK was declared inline on the column and MariaDB cannot drop an
-- inline (unnamed) constraint, so redefine the column and add a named one.
ALTER TABLE ssp_billable_outbox MODIFY COLUMN status VARCHAR(16) NOT NULL;
ALTER TABLE ssp_billable_outbox ADD CONSTRAINT chk_ssp_billable_outbox_status
  CHECK (status IN ('pending', 'sending', 'delivered', 'quarantined', 'rejected'));
ALTER TABLE ssp_billable_outbox
  ADD COLUMN resolved_at DATETIME(6) NULL,
  ADD COLUMN resolution_reason VARCHAR(512) NULL;
