-- Migration: 000005_dsp_spend_ledger_constraints.down.sql
-- Description: Revert the advertiser spend ledger invariants.

ALTER TABLE advertiser_spend_ledger
  DROP FOREIGN KEY fk_advertiser_spend_reversal,
  DROP KEY idx_advertiser_spend_reversal,
  DROP CONSTRAINT chk_advertiser_spend_reversal,
  DROP CONSTRAINT chk_advertiser_spend_type;
