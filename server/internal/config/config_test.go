package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestLoggingConfigDefaultsWhenSectionOmitted(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`server:
  port: 4888
  mode: debug

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

	if got := cfg.Logging.SelfLogPath(); got != "./data/self_logs.db" {
		t.Fatalf("self log path = %q, want default", got)
	}
	if got := cfg.Logging.SelfLogRetentionDays(); got != 7 {
		t.Fatalf("retention days = %d, want 7", got)
	}
	if got := cfg.Logging.SelfLogMaxEntries(); got != 1000000 {
		t.Fatalf("max entries = %d, want 1000000", got)
	}
	if got := cfg.Logging.SelfLogCleanupInterval(); got != time.Hour {
		t.Fatalf("cleanup interval = %s, want 1h", got)
	}
}

// 老部署的配置文件没有 quota 段时，升级后配额抓取必须仍默认开启——否则
// viper 会把 bool 反序列化成 false，静默停掉后台抓取；显式 false 才关闭。
func TestLoadQuotaDefaultsWhenSectionMissing(t *testing.T) {
	writeConfig := func(t *testing.T, body string) string {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(configPath, []byte(baseConfigYAML+body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return configPath
	}

	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("load without quota section: %v", err)
	}
	if !cfg.Quota.Enabled || !cfg.Quota.RunOnStartup {
		t.Fatalf("quota 段缺失时应默认开启，got enabled=%v run_on_startup=%v", cfg.Quota.Enabled, cfg.Quota.RunOnStartup)
	}

	cfg, err = Load(writeConfig(t, "\nquota:\n  enabled: false\n  run_on_startup: false\n"))
	if err != nil {
		t.Fatalf("load with explicit false: %v", err)
	}
	if cfg.Quota.Enabled || cfg.Quota.RunOnStartup {
		t.Fatalf("显式 false 应生效，got enabled=%v run_on_startup=%v", cfg.Quota.Enabled, cfg.Quota.RunOnStartup)
	}

	cfg, err = Load(writeConfig(t, "\nquota:\n  enabled: true\n  interval_minutes: 3\n"))
	if err != nil {
		t.Fatalf("load with partial quota section: %v", err)
	}
	if !cfg.Quota.Enabled || cfg.Quota.Interval() != 3*time.Minute {
		t.Fatalf("部分配置应逐项生效，got enabled=%v interval=%v", cfg.Quota.Enabled, cfg.Quota.Interval())
	}
}

// baseConfigYAML 是 quota 段之外的最小可加载配置。
const baseConfigYAML = `server:
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
`
