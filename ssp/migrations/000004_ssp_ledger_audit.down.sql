-- Migration: 000004_ssp_ledger_audit.down.sql
-- Description: Drop the publisher payable audit trail.

DROP TABLE IF EXISTS publisher_payable_audit;
