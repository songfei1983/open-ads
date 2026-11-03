-- Migration: 000005_dsp_spend_ledger_constraints.up.sql
-- Description: Enforce append-only advertiser spend ledger invariants.

ALTER TABLE advertiser_spend_ledger
  ADD CONSTRAINT chk_advertiser_spend_type CHECK (entry_type IN ('billable', 'reversal', 'adjustment')),
  ADD CONSTRAINT chk_advertiser_spend_reversal CHECK ((entry_type = 'reversal') = (reverses_entry_id IS NOT NULL)),
  ADD KEY idx_advertiser_spend_reversal (tenant_id, reverses_entry_id),
  ADD CONSTRAINT fk_advertiser_spend_reversal FOREIGN KEY (tenant_id, reverses_entry_id)
    REFERENCES advertiser_spend_ledger (tenant_id, entry_id);
