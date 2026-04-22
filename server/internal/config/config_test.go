package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverridesServerModeFromEnv(t *testing.T) {
	t.Setenv("FAST_SHIP_SERVER_MODE", "release")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`server:
  port: 4888
  mode: debug
  web_dist_dir: ""

database:
  path: ./data/fast_ship.db
  log_sql: false

jwt:
  secret: "change-me"
  expire_hours: 24

upload:
  max_file_size: 524288000
  storage_path: ./data/uploads

encryption:
  key: "12345678901234567890123456789012"

issues:
  auto_sync_enabled: true
  auto_sync_on_startup: true
  auto_sync_interval_minutes: 15
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Server.Mode != "release" {
		t.Fatalf("expected server mode release, got %q", cfg.Server.Mode)
	}
}

func TestLoadOverridesDatabaseLogSQLFromEnv(t *testing.T) {
	t.Setenv("FAST_SHIP_DATABASE_LOG_SQL", "true")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`server:
  port: 4888
  mode: debug
  web_dist_dir: ""

database:
  path: ./data/fast_ship.db
  log_sql: false

jwt:
  secret: "change-me"
  expire_hours: 24

upload:
  max_file_size: 524288000
  storage_path: ./data/uploads

encryption:
  key: "12345678901234567890123456789012"

issues:
  auto_sync_enabled: true
  auto_sync_on_startup: true
  auto_sync_interval_minutes: 15
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if !cfg.Database.LogSQL {
		t.Fatal("expected database log_sql true from env override")
	}
}
