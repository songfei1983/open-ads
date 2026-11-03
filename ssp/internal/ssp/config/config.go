package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration for the SSP service.
type Config struct {
	ServiceName   string
	Environment   string
	ConfigVersion string
	Port          int

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
	ValkeyTLS      bool

	// Internal service endpoints.
	DMPURL string
	DSPURL string

	// Control plane. The management API is tenant scoped: one ssp process
	// serves exactly one tenant and never trusts a tenant from the request.
	AdminTenantID string
	AdminToken    string
	InternalToken string
	// FinanceToken optionally authorises the ledger-only finance surface. When
	// unset the admin token also grants it (sandbox default).
	FinanceToken    string
	ConfigPublisher bool

	// AllowPrivateCallbacks waives the outbound private-address guard. It is a
	// sandbox-only switch: peers live on a private container network there.
	AllowPrivateCallbacks bool

	// Read-only creative asset store shared with the DSP writer.
	AssetStoreRoot string

	// Token keys. Without them the renderer and event tracker stay disabled
	// and filled responses cannot be produced.
	TokenKey        string
	TokenKeyVersion uint32
	RenderTokenTTL  time.Duration
	EventTokenTTL   time.Duration

	// DMP adapter. Subject tokens are HMAC-SHA256 digests; the DMP never sees
	// the publisher-scoped pseudonym.
	DMPNamespace    string
	DMPProviderID   string
	DMPTenantID     string
	SubjectHMACKey  string
	MaxSegments     int
	ClientRateLimit int

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
	valkeyTLS, err := getEnvBool("SSP_VALKEY_TLS")
	if err != nil {
		return nil, err
	}
	configPublisher, err := getEnvBool("SSP_CONFIG_PUBLISHER")
	if err != nil {
		return nil, err
	}
	allowPrivateCallbacks, err := getEnvBool("SSP_ALLOW_PRIVATE_CALLBACKS")
	if err != nil {
		return nil, err
	}
	tokenKeyVersion, err := getEnvInt("SSP_TOKEN_KEY_VERSION", 1)
	if err != nil {
		return nil, err
	}
	renderTokenTTL, err := getEnvInt("SSP_RENDER_TOKEN_TTL_SECONDS", 300)
	if err != nil {
		return nil, err
	}
	eventTokenTTL, err := getEnvInt("SSP_EVENT_TOKEN_TTL_SECONDS", 3600)
	if err != nil {
		return nil, err
	}
	maxSegments, err := getEnvInt("SSP_DMP_MAX_SEGMENTS", 10)
	if err != nil {
		return nil, err
	}
	clientRateLimit, err := getEnvInt("SSP_CLIENT_RATE_LIMIT_PER_SECOND", 0)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		ServiceName:     getEnv("SERVICE_NAME", "open-ads-ssp"),
		Environment:     getEnv("ENVIRONMENT", "development"),
		ConfigVersion:   getEnv("CONFIG_VERSION", "dev"),
		Port:            port,
		DBHost:          getEnv("SSP_DB_HOST", "127.0.0.1"),
		DBPort:          dbPort,
		DBUser:          getEnv("SSP_DB_USER", "ssp_user"),
		DBPassword:      getEnv("SSP_DB_PASSWORD", "ssp_pass"),
		DBName:          getEnv("SSP_DB_NAME", "ssp_db"),
		ValkeyAddr:      getEnv("VALKEY_ADDR", "127.0.0.1:6379"),
		ValkeyUser:      getEnv("SSP_VALKEY_USER", "ssp"),
		ValkeyPassword:  getEnv("SSP_VALKEY_PASSWORD", "ssp_valkey_password"),
		ValkeyTLS:       valkeyTLS,
		DMPURL:          strings.TrimRight(getEnv("DMP_URL", ""), "/"),
		DSPURL:          strings.TrimRight(getEnv("DSP_URL", ""), "/"),
		AdminTenantID:   getEnv("SSP_ADMIN_TENANT_ID", ""),
		AdminToken:      getEnv("SSP_ADMIN_TOKEN", ""),
		FinanceToken:    getEnv("SSP_FINANCE_TOKEN", ""),
		InternalToken:   getEnv("SSP_INTERNAL_TOKEN", ""),
		ConfigPublisher: configPublisher,

		// Sandbox-only: every peer sits on a private container network, so the
		// outbound private-address guard must be waived explicitly.
		AllowPrivateCallbacks: allowPrivateCallbacks,
		AssetStoreRoot:        getEnv("OPENADS_ASSET_STORE_ROOT", "/var/openads/assets/ssp-read"),
		TokenKey:              strings.TrimSpace(getEnv("SSP_TOKEN_KEY", "")),
		TokenKeyVersion:       uint32(tokenKeyVersion),
		RenderTokenTTL:        time.Duration(renderTokenTTL) * time.Second,
		EventTokenTTL:         time.Duration(eventTokenTTL) * time.Second,
		DMPNamespace:          getEnv("SSP_DMP_NAMESPACE", "publisher-account-v1"),
		DMPProviderID:         getEnv("SSP_DMP_PROVIDER_ID", ""),
		DMPTenantID:           getEnv("SSP_DMP_TENANT_ID", "tenant-default"),
		SubjectHMACKey:        strings.TrimSpace(getEnv("SSP_SUBJECT_HMAC_KEY", "")),
		MaxSegments:           maxSegments,
		ClientRateLimit:       clientRateLimit,
		AuctionTimeout:        time.Duration(auctionTimeout) * time.Millisecond,
		DMPTimeout:            time.Duration(dmpTimeout) * time.Millisecond,
		DSPTimeout:            time.Duration(dspTimeout) * time.Millisecond,
		LogLevel:              getEnv("LOG_LEVEL", "INFO"),
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
	if cfg.RenderTokenTTL <= 0 || cfg.EventTokenTTL <= 0 {
		return nil, fmt.Errorf("token TTLs must be positive")
	}
	if cfg.MaxSegments <= 0 || cfg.MaxSegments > 100 {
		return nil, fmt.Errorf("SSP_DMP_MAX_SEGMENTS must be between 1 and 100")
	}
	if cfg.ClientRateLimit < 0 {
		return nil, fmt.Errorf("SSP_CLIENT_RATE_LIMIT_PER_SECOND must not be negative")
	}
	if cfg.TokenKey != "" {
		key, err := hex.DecodeString(cfg.TokenKey)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("SSP_TOKEN_KEY must be 32 bytes of hex")
		}
	}
	if cfg.SubjectHMACKey != "" {
		key, err := hex.DecodeString(cfg.SubjectHMACKey)
		if err != nil || len(key) < 32 {
			return nil, fmt.Errorf("SSP_SUBJECT_HMAC_KEY must be at least 32 bytes of hex")
		}
	}
	if cfg.AdminTenantID != "" {
		tenant := strings.ToLower(strings.ReplaceAll(cfg.AdminTenantID, "-", ""))
		if len(tenant) != 32 {
			return nil, fmt.Errorf("SSP_ADMIN_TENANT_ID must be a UUID")
		}
		cfg.AdminTenantID = tenant
	}
	if cfg.AdminToken != "" {
		if cfg.AdminTenantID == "" || len(cfg.AdminToken) < 32 {
			return nil, fmt.Errorf("SSP_ADMIN_TOKEN requires SSP_ADMIN_TENANT_ID and at least 32 characters")
		}
	}
	if cfg.ConfigPublisher && cfg.AdminTenantID == "" {
		return nil, fmt.Errorf("SSP_CONFIG_PUBLISHER requires SSP_ADMIN_TENANT_ID")
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

func getEnvBool(key string) (bool, error) {
	val := os.Getenv(key)
	if val == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("invalid boolean environment variable %s: %w", key, err)
	}
	return parsed, nil
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
