-- Migration: 000001_init_dsp_schema.down.sql
-- Description: Rollback DSP database schema

DROP TABLE IF EXISTS advertiser_spend_ledger;
DROP TABLE IF EXISTS dsp_delivery_event;
DROP TABLE IF EXISTS dsp_notice_quarantine;
DROP TABLE IF EXISTS budget_reservation_settlement;
DROP TABLE IF EXISTS budget_lease;
DROP TABLE IF EXISTS budget_account;
DROP TABLE IF EXISTS line_item_creative;
DROP TABLE IF EXISTS creative;
DROP TABLE IF EXISTS line_item_targeting_version;
DROP TABLE IF EXISTS line_item;
DROP TABLE IF EXISTS campaign;
DROP TABLE IF EXISTS advertiser;
DROP TABLE IF EXISTS dsp_config_outbox;
DROP TABLE IF EXISTS tenant;
