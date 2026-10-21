-- Migration: 000001_init_dmp_schema.down.sql
-- Description: Rollback DMP ClickHouse schema

DROP TABLE IF EXISTS dmp_event;
DROP TABLE IF EXISTS dmp_identity_revocation_status;
DROP TABLE IF EXISTS dmp_identity_revocation;
DROP TABLE IF EXISTS dmp_segment_definition_version;
DROP TABLE IF EXISTS dmp_snapshot_publication;
DROP TABLE IF EXISTS dmp_membership_snapshot;
