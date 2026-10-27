-- Migration: 000003_dmp_snapshot_import.down.sql
-- Description: Remove the snapshot import job table

USE dmp;

DROP TABLE IF EXISTS dmp_snapshot_import_batch;
DROP TABLE IF EXISTS dmp_snapshot_import;
