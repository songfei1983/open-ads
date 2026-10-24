package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	ValkeyAddr     string
	ValkeyUser     string
	ValkeyPassword string

	// Timeouts
	AuctionTimeout time.Duration
	DMPTimeout     time.Duration
	DSPTimeout     time.Duration

	// Observability
	LogLevel string
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	port, err := getEnvInt("SSP_PORT", 8080)
	if err != nil {
		return nil, err
	}
	dbPort, err := getEnvInt("SSP_DB_PORT", 3306)
	if err != nil {
		return nil, err
	}
	auctionTimeout, err := getEnvInt("AUCTION_TIMEOUT_MS", 80)
	if err != nil {
		return nil, err
	}
	dmpTimeout, err := getEnvInt("DMP_TIMEOUT_MS", 15)
	if err != nil {
		return nil, err
	}
	dspTimeout, err := getEnvInt("DSP_TIMEOUT_MS", 50)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		ServiceName:    getEnv("SERVICE_NAME", "open-ads-ssp"),
		Environment:    getEnv("ENVIRONMENT", "development"),
		Port:           port,
		DBHost:         getEnv("SSP_DB_HOST", "127.0.0.1"),
		DBPort:         dbPort,
		DBUser:         getEnv("SSP_DB_USER", "ssp_user"),
		DBPassword:     getEnv("SSP_DB_PASSWORD", "ssp_pass"),
		DBName:         getEnv("SSP_DB_NAME", "ssp_db"),
		ValkeyAddr:     getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		ValkeyUser:     getEnv("SSP_VALKEY_USER", "ssp"),
		ValkeyPassword: getEnv("SSP_VALKEY_PASSWORD", "ssp_valkey_password"),
		AuctionTimeout: time.Duration(auctionTimeout) * time.Millisecond,
		DMPTimeout:     time.Duration(dmpTimeout) * time.Millisecond,
		DSPTimeout:     time.Duration(dspTimeout) * time.Millisecond,
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
	if cfg.AuctionTimeout <= 0 || cfg.DMPTimeout <= 0 || cfg.DSPTimeout <= 0 {
		return nil, fmt.Errorf("timeouts must be positive")
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
