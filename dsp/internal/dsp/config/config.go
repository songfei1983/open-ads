package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds runtime configuration for the DSP service.
type Config struct {
	ServiceName string
	Environment string
	Port        int

	// Database configuration (MariaDB dsp_db)
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string

	// Valkey configuration
	ValkeyAddr string

	// Bidder timeouts
	BidTimeout time.Duration

	// Observability
	LogLevel string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		ServiceName: getEnv("SERVICE_NAME", "open-ads-dsp"),
		Environment: getEnv("ENVIRONMENT", "development"),
		Port:        getEnvInt("DSP_PORT", 8081),
		DBHost:      getEnv("DSP_DB_HOST", "127.0.0.1"),
		DBPort:      getEnvInt("DSP_DB_PORT", 3306),
		DBUser:      getEnv("DSP_DB_USER", "dsp_user"),
		DBPassword:  getEnv("DSP_DB_PASSWORD", "dsp_pass"),
		DBName:      getEnv("DSP_DB_NAME", "dsp_db"),
		ValkeyAddr:  getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		BidTimeout:  time.Duration(getEnvInt("DSP_BID_TIMEOUT_MS", 50)) * time.Millisecond,
		LogLevel:    getEnv("LOG_LEVEL", "INFO"),
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", cfg.Port)
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}
