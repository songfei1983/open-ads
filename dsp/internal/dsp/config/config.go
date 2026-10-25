package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
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

	// Admin API is enabled only when both fields are configured.
	AdminTenantID   string
	AdminToken      string
	ConfigPublisher bool
	AssetStoreRoot  string

	// Valkey configuration
	ValkeyAddr        string
	ValkeyUser        string
	ValkeyPassword    string
	ValkeyTLS         bool
	CreativeWhitelist []domain.WhiteListEntry

	// Budget reservation notice and token configuration.
	BaseNoticeURL   string
	LeaseKeyVersion string
	LeaseTokenKey   string

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
	valkeyTLS, err := getEnvBool("DSP_VALKEY_TLS", false)
	if err != nil {
		return nil, err
	}
	configPublisher, err := getEnvBool("DSP_CONFIG_PUBLISHER", false)
	if err != nil {
		return nil, err
	}
	creativeWhitelist, err := parseCreativeWhitelist(os.Getenv("DSP_CREATIVE_WHITELIST"))
	if err != nil {
		return nil, err
	}
	// DSP_VALKEY_ADDR is the canonical key; VALKEY_ADDR is a legacy fallback.
	valkeyAddr := os.Getenv("DSP_VALKEY_ADDR")
	if strings.TrimSpace(valkeyAddr) == "" {
		valkeyAddr = getEnv("VALKEY_ADDR", "127.0.0.1:6379")
	}
	leaseKeyVersion := getEnv("DSP_LEASE_KEY_VERSION", "v1")
	leaseTokenKey := strings.ToLower(strings.TrimSpace(os.Getenv("DSP_LEASE_TOKEN_KEY")))
	if leaseTokenKey != "" {
		if _, err := hex.DecodeString(leaseTokenKey); err != nil || len(leaseTokenKey) != 64 {
			return nil, fmt.Errorf("DSP_LEASE_TOKEN_KEY must be 64 hex characters (32 bytes)")
		}
	}
	cfg := &Config{
		ServiceName:       getEnv("SERVICE_NAME", "open-ads-dsp"),
		Environment:       getEnv("ENVIRONMENT", "development"),
		ConfigVersion:     getEnv("CONFIG_VERSION", "dev"),
		Port:              port,
		DBHost:            getEnv("DSP_DB_HOST", "127.0.0.1"),
		DBPort:            dbPort,
		DBUser:            getEnv("DSP_DB_USER", "dsp_user"),
		DBPassword:        getEnv("DSP_DB_PASSWORD", "dsp_pass"),
		DBName:            getEnv("DSP_DB_NAME", "dsp_db"),
		AdminTenantID:     getEnv("DSP_ADMIN_TENANT_ID", ""),
		AdminToken:        getEnv("DSP_ADMIN_TOKEN", ""),
		ConfigPublisher:   configPublisher,
		AssetStoreRoot:    getEnv("OPENADS_ASSET_STORE_ROOT", "/var/openads/assets/dsp-write"),
		ValkeyAddr:        valkeyAddr,
		ValkeyUser:        getEnv("DSP_VALKEY_USER", "dsp"),
		ValkeyPassword:    getEnv("DSP_VALKEY_PASSWORD", "dsp_valkey_password"),
		ValkeyTLS:         valkeyTLS,
		CreativeWhitelist: creativeWhitelist,
		BaseNoticeURL:     strings.TrimRight(getEnv("DSP_BASE_NOTICE_URL", fmt.Sprintf("http://127.0.0.1:%d", port)), "/"),
		LeaseKeyVersion:   leaseKeyVersion,
		LeaseTokenKey:     leaseTokenKey,
		BidTimeout:        time.Duration(bidTimeout) * time.Millisecond,
		LogLevel:          getEnv("LOG_LEVEL", "INFO"),
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
	adminEnabled := cfg.AdminToken != ""
	if cfg.AdminTenantID != "" {
		if err := domain.ValidateUUID32(strings.ToLower(strings.ReplaceAll(cfg.AdminTenantID, "-", ""))); err != nil {
			return nil, fmt.Errorf("invalid DSP_ADMIN_TENANT_ID: %w", err)
		}
	}
	if cfg.AdminToken != "" && (cfg.AdminTenantID == "" || len(cfg.AdminToken) < 32) {
		return nil, fmt.Errorf("DSP_ADMIN_TOKEN requires DSP_ADMIN_TENANT_ID and must be at least 32 characters")
	}
	if (adminEnabled || cfg.ConfigPublisher) && cfg.AdminTenantID == "" {
		return nil, fmt.Errorf("DSP_ADMIN_TENANT_ID is required when enabling admin API or config publisher")
	}

	return cfg, nil
}

func parseCreativeWhitelist(raw string) ([]domain.WhiteListEntry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	entries := strings.Split(raw, ",")
	result := make([]domain.WhiteListEntry, 0, len(entries))
	for _, entry := range entries {
		host, mime, ok := strings.Cut(strings.TrimSpace(entry), "=")
		host = strings.ToLower(strings.TrimSpace(host))
		mime = strings.ToLower(strings.TrimSpace(mime))
		if !ok || host == "" || strings.ContainsAny(host, "/?#@ \t\r\n") || strings.Contains(host, ":") {
			return nil, fmt.Errorf("invalid DSP_CREATIVE_WHITELIST entry %q: expected hostname=mime", entry)
		}
		if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
			return nil, fmt.Errorf("invalid DSP_CREATIVE_WHITELIST hostname %q", host)
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, fmt.Errorf("invalid DSP_CREATIVE_WHITELIST hostname %q", host)
			}
			for _, character := range label {
				if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
					return nil, fmt.Errorf("invalid DSP_CREATIVE_WHITELIST hostname %q", host)
				}
			}
		}
		if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
			return nil, fmt.Errorf("unsupported DSP_CREATIVE_WHITELIST media type %q", mime)
		}
		result = append(result, domain.WhiteListEntry{Host: host, MIME: mime})
	}
	return result, nil
}

func getEnvBool(key string, defaultValue bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean environment variable %s: %w", key, err)
	}
	return parsed, nil
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
