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

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("DMP_PORT", "not-a-number")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestLoadControlPlaneConfig(t *testing.T) {
	t.Setenv("DMP_ADMIN_TOKEN", "dmp-admin-token-0123456789abcdef")
	t.Setenv("DMP_ADMIN_TENANT_ID", "00000000000000000000000000000001")
	t.Setenv("DMP_ADMIN_ACTOR", "sandbox-operator")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AdminToken != "dmp-admin-token-0123456789abcdef" || cfg.AdminTenantID != "00000000000000000000000000000001" {
		t.Fatalf("control plane configuration not loaded: %+v", cfg)
	}
	if cfg.AdminActor != "sandbox-operator" {
		t.Fatalf("unexpected actor: %q", cfg.AdminActor)
	}
}

func TestLoadRequiresTenantWithToken(t *testing.T) {
	t.Setenv("DMP_ADMIN_TOKEN", "dmp-admin-token-0123456789abcdef")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected token without tenant to be rejected")
	}
}

func TestLoadRejectsShortAdminToken(t *testing.T) {
	t.Setenv("DMP_ADMIN_TOKEN", "short-token")
	t.Setenv("DMP_ADMIN_TENANT_ID", "00000000000000000000000000000001")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected short admin token to be rejected")
	}
}

func TestLoadInternalToken(t *testing.T) {
	t.Setenv("DMP_INTERNAL_TOKEN", "dmp-internal-token-0123456789abcdef")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InternalToken != "dmp-internal-token-0123456789abcdef" {
		t.Fatalf("internal token not loaded: %q", cfg.InternalToken)
	}

	t.Setenv("DMP_INTERNAL_TOKEN", "short")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected short internal token to be rejected")
	}
}

func TestLoadRejectsInvalidAdminTenant(t *testing.T) {
	t.Setenv("DMP_ADMIN_TOKEN", "dmp-admin-token-0123456789abcdef")
	t.Setenv("DMP_ADMIN_TENANT_ID", "not-a-uuid")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid admin tenant to be rejected")
	}
}
