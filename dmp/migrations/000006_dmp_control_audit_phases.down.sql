-- Down migration: 000006_dmp_control_audit_phases
USE dmp;

ALTER TABLE dmp_control_audit DROP INDEX IF EXISTS idx_audit_phase;
ALTER TABLE dmp_control_audit DROP COLUMN IF EXISTS intent_id;
ALTER TABLE dmp_control_audit DROP COLUMN IF EXISTS phase;
