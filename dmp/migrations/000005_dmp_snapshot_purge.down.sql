-- Migration: 000005_dmp_snapshot_purge.down.sql
-- Description: Remove the snapshot purge ledger

USE dmp;

DROP TABLE IF EXISTS dmp_snapshot_purge;
