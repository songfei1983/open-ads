package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds runtime configuration for the DMP service.
type Config struct {
	ServiceName string
	Environment string
	Port        int

	// ClickHouse configuration
	ClickHouseAddr     string
	ClickHouseUser     string
	ClickHousePassword string
	ClickHouseDatabase string

	// Valkey Deny Gate configuration
	ValkeyAddr string

	// Observability
	LogLevel string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		ServiceName:        getEnv("SERVICE_NAME", "open-ads-dmp"),
		Environment:        getEnv("ENVIRONMENT", "development"),
		Port:               getEnvInt("DMP_PORT", 8082),
		ClickHouseAddr:     getEnv("CLICKHOUSE_ADDR", "127.0.0.1:9000"),
		ClickHouseUser:     getEnv("CLICKHOUSE_USER", "default"),
		ClickHousePassword: getEnv("CLICKHOUSE_PASSWORD", ""),
		ClickHouseDatabase: getEnv("CLICKHOUSE_DATABASE", "dmp"),
		ValkeyAddr:         getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		LogLevel:           getEnv("LOG_LEVEL", "INFO"),
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
