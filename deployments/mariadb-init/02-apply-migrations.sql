-- Apply the checked-in schemas when a fresh local MariaDB volume is created.
USE ssp_db;
SOURCE /migrations/ssp/000001_init_ssp_schema.up.sql;
USE dsp_db;
SOURCE /migrations/dsp/000001_init_dsp_schema.up.sql;
