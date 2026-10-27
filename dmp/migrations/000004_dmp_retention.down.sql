-- Migration: 000004_dmp_retention.down.sql
-- Description: Remove the event TTL

USE dmp;

ALTER TABLE dmp_event REMOVE TTL;
