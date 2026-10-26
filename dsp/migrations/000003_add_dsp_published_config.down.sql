ALTER TABLE dsp_config_outbox
  DROP INDEX idx_dsp_outbox_retry,
  DROP COLUMN next_attempt_at;

DROP TABLE dsp_published_config;
DROP TABLE dsp_config_manifest_clock;
