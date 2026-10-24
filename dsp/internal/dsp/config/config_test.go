package config_test

import (
	"testing"
	"time"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error loading defaults: %v", err)
	}

	if cfg.Port != 8081 {
		t.Errorf("expected default port 8081, got %d", cfg.Port)
	}
	if cfg.DBName != "dsp_db" {
		t.Errorf("expected DBName dsp_db, got %s", cfg.DBName)
	}
	if cfg.BidTimeout != 50*time.Millisecond {
		t.Errorf("expected BidTimeout 50ms, got %v", cfg.BidTimeout)
	}
}

func TestLoadRejectsInvalidBidTimeout(t *testing.T) {
	t.Setenv("DSP_BID_TIMEOUT_MS", "not-a-number")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid timeout error")
	}
}
