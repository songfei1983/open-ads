package config_test

import (
	"testing"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error loading defaults: %v", err)
	}

	if cfg.Port != 8082 {
		t.Errorf("expected default port 8082, got %d", cfg.Port)
	}
	if cfg.ClickHouseDatabase != "dmp" {
		t.Errorf("expected database dmp, got %s", cfg.ClickHouseDatabase)
	}
}
