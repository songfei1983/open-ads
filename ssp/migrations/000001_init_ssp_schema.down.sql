-- Migration: 000001_init_ssp_schema.down.sql
-- Description: Rollback SSP database schema

DROP TABLE IF EXISTS publisher_payable_ledger;
DROP TABLE IF EXISTS ssp_billable_outbox;
DROP TABLE IF EXISTS ssp_delivery_event;
DROP TABLE IF EXISTS partner_identity_link;
DROP TABLE IF EXISTS ssp_identity_revocation;
DROP TABLE IF EXISTS publisher_subject;
DROP TABLE IF EXISTS ssp_config_outbox;
DROP TABLE IF EXISTS dsp_endpoint;
DROP TABLE IF EXISTS supply_endpoint;
DROP TABLE IF EXISTS placement;
DROP TABLE IF EXISTS auction_policy;
DROP TABLE IF EXISTS property;
DROP TABLE IF EXISTS publisher;
DROP TABLE IF EXISTS tenant;
