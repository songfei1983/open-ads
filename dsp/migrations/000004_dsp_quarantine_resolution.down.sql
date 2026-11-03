-- Migration: 000004_dsp_quarantine_resolution.down.sql
-- Description: Restore the original quarantined notice status domain

UPDATE dsp_notice_quarantine SET status = 'rejected' WHERE status = 'resolved';
ALTER TABLE dsp_notice_quarantine DROP CONSTRAINT chk_dsp_quarantine_status;
ALTER TABLE dsp_notice_quarantine MODIFY COLUMN status VARCHAR(16) NOT NULL
  CHECK (status IN ('pending', 'accepted', 'rejected'));
