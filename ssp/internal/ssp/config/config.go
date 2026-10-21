package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds runtime configuration for the SSP service.
type Config struct {
	ServiceName string
	Environment string
	Port        int

	// Database configuration (MariaDB ssp_db)
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string

	// Valkey configuration
	ValkeyAddr string

	// Timeouts
	AuctionTimeout time.Duration
	DMPTimeout     time.Duration
	DSPTimeout     time.Duration

	// Observability
	LogLevel string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		ServiceName:    getEnv("SERVICE_NAME", "open-ads-ssp"),
		Environment:    getEnv("ENVIRONMENT", "development"),
		Port:           getEnvInt("SSP_PORT", 8080),
		DBHost:         getEnv("SSP_DB_HOST", "127.0.0.1"),
		DBPort:         getEnvInt("SSP_DB_PORT", 3306),
		DBUser:         getEnv("SSP_DB_USER", "ssp_user"),
		DBPassword:     getEnv("SSP_DB_PASSWORD", "ssp_pass"),
		DBName:         getEnv("SSP_DB_NAME", "ssp_db"),
		ValkeyAddr:     getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		AuctionTimeout: time.Duration(getEnvInt("AUCTION_TIMEOUT_MS", 80)) * time.Millisecond,
		DMPTimeout:     time.Duration(getEnvInt("DMP_TIMEOUT_MS", 15)) * time.Millisecond,
		DSPTimeout:     time.Duration(getEnvInt("DSP_TIMEOUT_MS", 50)) * time.Millisecond,
		LogLevel:       getEnv("LOG_LEVEL", "INFO"),
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
