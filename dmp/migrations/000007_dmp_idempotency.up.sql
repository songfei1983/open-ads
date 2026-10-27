-- Migration: 000007_dmp_idempotency.up.sql
-- Description: Store idempotency keys and their responses so a retried import or
-- revocation replays the original answer instead of acting twice.

USE dmp;

CREATE TABLE IF NOT EXISTS dmp_idempotency
(
  tenant_id UUID,
  idempotency_key String,
  route LowCardinality(String),
  request_hash String,
  response_status UInt16,
  response_body String,
  recorded_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(recorded_at)
ORDER BY (tenant_id, idempotency_key, route)
-- Retries happen within minutes; a day is long enough and bounds storage.
TTL toDateTime(recorded_at) + INTERVAL 24 HOUR;
