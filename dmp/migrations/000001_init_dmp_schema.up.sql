-- Migration: 000001_init_dmp_schema.up.sql
-- Description: Initialize DMP ClickHouse schema and MergeTree tables

CREATE TABLE IF NOT EXISTS dmp_membership_snapshot
(
  tenant_id UUID,
  provider_id String,
  namespace LowCardinality(String),
  snapshot_id UUID,
  snapshot_month Date,
  subject_token FixedString(64),
  subject_token_key_version UInt16,
  segment_id String,
  ingested_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(snapshot_month)
ORDER BY (tenant_id, snapshot_id, namespace, subject_token, segment_id);

CREATE TABLE IF NOT EXISTS dmp_snapshot_publication
(
  tenant_id UUID,
  provider_id String,
  namespace LowCardinality(String),
  snapshot_id UUID,
  created_at DateTime64(3, 'UTC'),
  published_at DateTime64(3, 'UTC'),
  expected_rows UInt64,
  checksum String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (tenant_id, provider_id, namespace, published_at, snapshot_id);

CREATE TABLE IF NOT EXISTS dmp_segment_definition_version
(
  tenant_id UUID,
  provider_id String,
  segment_id String,
  version UInt64,
  name String,
  status LowCardinality(String),
  valid_from DateTime64(3, 'UTC'),
  written_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, provider_id, segment_id, version);

CREATE TABLE IF NOT EXISTS dmp_identity_revocation
(
  tenant_id UUID,
  namespace LowCardinality(String),
  subject_token FixedString(64),
  subject_token_key_version UInt16,
  revocation_id UUID,
  effective_at DateTime64(3, 'UTC'),
  received_at DateTime64(3, 'UTC'),
  reason_code LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(received_at)
ORDER BY (tenant_id, namespace, subject_token, effective_at, revocation_id);

CREATE TABLE IF NOT EXISTS dmp_identity_revocation_status
(
  tenant_id UUID,
  revocation_id UUID,
  status_version UInt64,
  online_status LowCardinality(String),
  physical_delete_status LowCardinality(String),
  covered_key_versions Array(UInt16),
  gate_epoch UInt64,
  drained_lookup_count UInt64,
  updated_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, revocation_id, status_version);

CREATE TABLE IF NOT EXISTS dmp_event
(
  tenant_id UUID,
  event_date Date,
  event_id UUID,
  event_type LowCardinality(String),
  provider_id String,
  namespace LowCardinality(String),
  subject_token FixedString(64),
  segment_id String,
  occurred_at DateTime64(3, 'UTC'),
  ingested_at DateTime64(3, 'UTC'),
  schema_version UInt16
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(event_date)
ORDER BY (tenant_id, provider_id, namespace, subject_token, occurred_at, event_id);
