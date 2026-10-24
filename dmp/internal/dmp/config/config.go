package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration for the DMP service.
type Config struct {
	ServiceName   string
	Environment   string
	ConfigVersion string
	Port          int

	// ClickHouse configuration
	ClickHouseAddr     string
	ClickHouseUser     string
	ClickHousePassword string
	ClickHouseDatabase string
	RequestTimeout     time.Duration

	// Valkey Deny Gate configuration
	ValkeyAddr     string
	ValkeyUser     string
	ValkeyPassword string

	// Observability
	LogLevel string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	port, err := getEnvInt("DMP_PORT", 8082)
	if err != nil {
		return nil, err
	}
	requestTimeout, err := getEnvInt("DMP_REQUEST_TIMEOUT_MS", 15)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		ServiceName:        getEnv("SERVICE_NAME", "open-ads-dmp"),
		Environment:        getEnv("ENVIRONMENT", "development"),
		ConfigVersion:      getEnv("CONFIG_VERSION", "dev"),
		Port:               port,
		ClickHouseAddr:     getEnv("CLICKHOUSE_ADDR", "127.0.0.1:9000"),
		ClickHouseUser:     getEnv("CLICKHOUSE_USER", "dmp_user"),
		ClickHousePassword: getEnv("CLICKHOUSE_PASSWORD", "dmp_clickhouse_password"),
		ClickHouseDatabase: getEnv("CLICKHOUSE_DATABASE", "dmp"),
		ValkeyAddr:         getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		ValkeyUser:         getEnv("DMP_VALKEY_USER", "dmp"),
		ValkeyPassword:     getEnv("DMP_VALKEY_PASSWORD", "dmp_valkey_password"),
		RequestTimeout:     time.Duration(requestTimeout) * time.Millisecond,
		LogLevel:           getEnv("LOG_LEVEL", "INFO"),
	}
	if !validLogLevel(cfg.LogLevel) {
		return nil, fmt.Errorf("invalid log level: %q", cfg.LogLevel)
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", cfg.Port)
	}
	if cfg.RequestTimeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}

	return cfg, nil
}

func validLogLevel(level string) bool {
	switch strings.ToUpper(level) {
	case "DEBUG", "INFO", "WARN", "ERROR":
		return true
	default:
		return false
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) (int, error) {
	if val := os.Getenv(key); val != "" {
		i, err := strconv.Atoi(val)
		if err != nil {
			return 0, fmt.Errorf("invalid integer environment variable %s: %w", key, err)
		}
		return i, nil
	}
	return defaultVal, nil
}
