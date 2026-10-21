-- Initialize isolated databases for SSP and DSP
CREATE DATABASE IF NOT EXISTS ssp_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS dsp_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Dedicated least-privilege users
CREATE USER IF NOT EXISTS 'ssp_user'@'%' IDENTIFIED BY 'ssp_pass';
GRANT ALL PRIVILEGES ON ssp_db.* TO 'ssp_user'@'%';

CREATE USER IF NOT EXISTS 'dsp_user'@'%' IDENTIFIED BY 'dsp_pass';
GRANT ALL PRIVILEGES ON dsp_db.* TO 'dsp_user'@'%';

FLUSH PRIVILEGES;
