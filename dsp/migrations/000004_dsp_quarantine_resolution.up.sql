-- Migration: 000004_dsp_quarantine_resolution.up.sql
-- Description: Allow quarantined notices to be resolved by an operator
--
-- The original CHECK was declared inline on the column, and MariaDB cannot drop
-- an inline (unnamed) check constraint. Redefining the column without the CHECK
-- removes it, and a named table-level constraint replaces it.

ALTER TABLE dsp_notice_quarantine MODIFY COLUMN status VARCHAR(16) NOT NULL;
ALTER TABLE dsp_notice_quarantine ADD CONSTRAINT chk_dsp_quarantine_status
  CHECK (status IN ('pending', 'accepted', 'rejected', 'resolved'));
