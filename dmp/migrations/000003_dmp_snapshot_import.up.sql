-- Migration: 000003_dmp_snapshot_import.up.sql
-- Description: Track snapshot import jobs as append-only state versions

USE dmp;

CREATE TABLE IF NOT EXISTS dmp_snapshot_import
(
  tenant_id UUID,
  snapshot_id UUID,
  provider_id String,
  namespace LowCardinality(String),
  schema_version UInt16,
  source_version String,
  expected_rows UInt64,
  expected_checksum String,
  submitted_rows UInt64,
  duplicate_rows UInt64,
  stored_rows UInt64,
  status LowCardinality(String),
  failure_reason String,
  state_version UInt64,
  created_at DateTime64(3, 'UTC'),
  updated_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, snapshot_id, state_version);

-- One row per accepted batch: verification derives submitted/duplicate totals
-- from these rows, so concurrent appends cannot lose counts.
CREATE TABLE IF NOT EXISTS dmp_snapshot_import_batch
(
  tenant_id UUID,
  snapshot_id UUID,
  batch_id UUID,
  submitted_rows UInt64,
  stored_rows UInt64,
  idempotent_rows UInt64,
  duplicate_rows UInt64,
  received_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, snapshot_id, received_at, batch_id);
