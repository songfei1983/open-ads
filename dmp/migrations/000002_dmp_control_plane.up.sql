-- Migration: 000002_dmp_control_plane.up.sql
-- Description: Add append-only, versioned DMP control-plane definitions and audit trail

USE dmp;

-- Identity namespace definitions. ClickHouse cannot enforce uniqueness, so one
-- row per (tenant, namespace, version) is written and the newest version with
-- the lowest revision_id is the authoritative definition.
CREATE TABLE IF NOT EXISTS dmp_namespace_version
(
  tenant_id UUID,
  namespace LowCardinality(String),
  version UInt64,
  revision_id UUID,
  display_name String,
  hashing_algorithm LowCardinality(String),
  subject_key_version UInt16,
  retention_months UInt8,
  status LowCardinality(String),
  written_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, namespace, version);

CREATE TABLE IF NOT EXISTS dmp_provider_version
(
  tenant_id UUID,
  provider_id String,
  version UInt64,
  revision_id UUID,
  display_name String,
  namespace LowCardinality(String),
  license_version String,
  status LowCardinality(String),
  written_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
ORDER BY (tenant_id, provider_id, version);

-- Segment definitions already exist from 000001; versioning needs a revision
-- identifier so concurrent writers of the same version are detectable.
ALTER TABLE dmp_segment_definition_version
  ADD COLUMN IF NOT EXISTS revision_id UUID DEFAULT generateUUIDv4();

ALTER TABLE dmp_segment_definition_version
  ADD COLUMN IF NOT EXISTS description String DEFAULT '';

CREATE TABLE IF NOT EXISTS dmp_control_audit
(
  tenant_id UUID,
  audit_id UUID,
  occurred_at DateTime64(3, 'UTC'),
  actor String,
  action LowCardinality(String),
  resource_type LowCardinality(String),
  resource_id String,
  resource_version UInt64,
  request_id String,
  outcome LowCardinality(String),
  detail String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (tenant_id, occurred_at, audit_id);
