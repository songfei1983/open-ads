package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration for the DSP service.
type Config struct {
	ServiceName   string
	Environment   string
	ConfigVersion string
	Port          int

	// Database configuration (MariaDB dsp_db)
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string

	// Valkey configuration
	ValkeyAddr     string
	ValkeyUser     string
	ValkeyPassword string

	// Bidder timeouts
	BidTimeout time.Duration

	// Observability
	LogLevel string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	port, err := getEnvInt("DSP_PORT", 8081)
	if err != nil {
		return nil, err
	}
	dbPort, err := getEnvInt("DSP_DB_PORT", 3306)
	if err != nil {
		return nil, err
	}
	bidTimeout, err := getEnvInt("DSP_BID_TIMEOUT_MS", 50)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		ServiceName:    getEnv("SERVICE_NAME", "open-ads-dsp"),
		Environment:    getEnv("ENVIRONMENT", "development"),
		ConfigVersion:  getEnv("CONFIG_VERSION", "dev"),
		Port:           port,
		DBHost:         getEnv("DSP_DB_HOST", "127.0.0.1"),
		DBPort:         dbPort,
		DBUser:         getEnv("DSP_DB_USER", "dsp_user"),
		DBPassword:     getEnv("DSP_DB_PASSWORD", "dsp_pass"),
		DBName:         getEnv("DSP_DB_NAME", "dsp_db"),
		ValkeyAddr:     getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		ValkeyUser:     getEnv("DSP_VALKEY_USER", "dsp"),
		ValkeyPassword: getEnv("DSP_VALKEY_PASSWORD", "dsp_valkey_password"),
		BidTimeout:     time.Duration(bidTimeout) * time.Millisecond,
		LogLevel:       getEnv("LOG_LEVEL", "INFO"),
	}
	if !validLogLevel(cfg.LogLevel) {
		return nil, fmt.Errorf("invalid log level: %q", cfg.LogLevel)
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", cfg.Port)
	}
	if cfg.DBPort <= 0 || cfg.DBPort > 65535 {
		return nil, fmt.Errorf("invalid database port: %d", cfg.DBPort)
	}
	if cfg.BidTimeout <= 0 {
		return nil, fmt.Errorf("bid timeout must be positive")
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
