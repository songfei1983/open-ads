-- Migration: 000002_dmp_control_plane.down.sql
-- Description: Remove DMP control-plane definitions and audit trail

USE dmp;

ALTER TABLE dmp_segment_definition_version DROP COLUMN IF EXISTS description;
ALTER TABLE dmp_segment_definition_version DROP COLUMN IF EXISTS revision_id;

DROP TABLE IF EXISTS dmp_control_audit;
DROP TABLE IF EXISTS dmp_provider_version;
DROP TABLE IF EXISTS dmp_namespace_version;
