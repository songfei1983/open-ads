-- Migration: 000004_dmp_retention.up.sql
-- Description: Enforce the approved 12-month retention on DMP analytic events

USE dmp;

-- DMP_EVENT keeps pseudonymous analytics for at most 12 months (approved
-- baseline). Membership snapshots are deleted by controlled mutation keyed by
-- snapshot_id instead, so a single snapshot never touches a shared partition.
ALTER TABLE dmp_event MODIFY TTL event_date + INTERVAL 365 DAY;
