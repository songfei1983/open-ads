-- Migration: 000005_dmp_snapshot_purge.up.sql
-- Description: Record revocation purges per snapshot so row counts stay explainable

USE dmp;

CREATE TABLE IF NOT EXISTS dmp_snapshot_purge
(
  tenant_id UUID,
  snapshot_id UUID,
  namespace LowCardinality(String),
  subject_token FixedString(64),
  purged_rows UInt64,
  reason_code LowCardinality(String),
  purged_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, snapshot_id, purged_at, subject_token);
