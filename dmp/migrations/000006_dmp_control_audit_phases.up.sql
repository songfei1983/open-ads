-- Migration: 000006_dmp_control_audit_phases.up.sql
-- Description: Record control-plane attempts as an intent row plus an outcome
-- row so an attempt without an outcome is detectable as "result unknown".

USE dmp;

ALTER TABLE dmp_control_audit
  ADD COLUMN IF NOT EXISTS phase LowCardinality(String) DEFAULT 'outcome',
  ADD COLUMN IF NOT EXISTS intent_id UUID DEFAULT '00000000-0000-0000-0000-000000000000';

-- Unfinished-intent sweeps filter on intent rows only.
ALTER TABLE dmp_control_audit
  ADD INDEX IF NOT EXISTS idx_audit_phase phase TYPE set(4) GRANULARITY 4;
