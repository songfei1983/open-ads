package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error loading defaults: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Port)
	}
	if cfg.DBName != "ssp_db" {
		t.Errorf("expected DBName ssp_db, got %s", cfg.DBName)
	}
	if cfg.AuctionTimeout != 80*time.Millisecond {
		t.Errorf("expected AuctionTimeout 80ms, got %v", cfg.AuctionTimeout)
	}
}

func TestLoadCustomEnv(t *testing.T) {
	_ = os.Setenv("SSP_PORT", "9090")
	_ = os.Setenv("SSP_DB_NAME", "custom_ssp")
	defer func() {
		_ = os.Unsetenv("SSP_PORT")
		_ = os.Unsetenv("SSP_DB_NAME")
	}()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Port)
	}
	if cfg.DBName != "custom_ssp" {
		t.Errorf("expected DBName custom_ssp, got %s", cfg.DBName)
	}
}
