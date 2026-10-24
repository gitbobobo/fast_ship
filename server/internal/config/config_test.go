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

// INT-168：review_media.trusted_private_hosts 可信内网主机名单——yaml 列表
// 与环境变量（逗号分隔）两条路都要进结构体；段落缺失时为空（不跳过内网
// IP 检查的旧行为）。
func TestLoadReviewMediaTrustedPrivateHosts(t *testing.T) {
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
		t.Fatalf("load without review_media section: %v", err)
	}
	if len(cfg.ReviewMedia.TrustedPrivateHosts) != 0 {
		t.Fatalf("段落缺失时名单应为空，got %v", cfg.ReviewMedia.TrustedPrivateHosts)
	}

	cfg, err = Load(writeConfig(t, "\nreview_media:\n  trusted_private_hosts: [\"uiv.internal.example.com\", \"IMG.LOCAL\"]\n"))
	if err != nil {
		t.Fatalf("load with review_media section: %v", err)
	}
	want := []string{"uiv.internal.example.com", "IMG.LOCAL"}
	if len(cfg.ReviewMedia.TrustedPrivateHosts) != len(want) {
		t.Fatalf("trusted_private_hosts = %v, want %v", cfg.ReviewMedia.TrustedPrivateHosts, want)
	}
	for i := range want {
		if cfg.ReviewMedia.TrustedPrivateHosts[i] != want[i] {
			t.Fatalf("trusted_private_hosts = %v, want %v", cfg.ReviewMedia.TrustedPrivateHosts, want)
		}
	}
}

// 环境变量覆盖：FAST_SHIP_REVIEW_MEDIA_TRUSTED_PRIVATE_HOSTS 逗号分隔。
func TestLoadReviewMediaTrustedHostsFromEnv(t *testing.T) {
	t.Setenv("FAST_SHIP_REVIEW_MEDIA_TRUSTED_PRIVATE_HOSTS", "uiv.local,img.local")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(baseConfigYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"uiv.local", "img.local"}
	if len(cfg.ReviewMedia.TrustedPrivateHosts) != len(want) ||
		cfg.ReviewMedia.TrustedPrivateHosts[0] != want[0] ||
		cfg.ReviewMedia.TrustedPrivateHosts[1] != want[1] {
		t.Fatalf("trusted_private_hosts = %v, want %v", cfg.ReviewMedia.TrustedPrivateHosts, want)
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

// INT-137：机器人运行参数（流超时、停止宽限、重试、熔断窗口）的默认值、
// 覆盖与校验。默认值本身就是行为契约（90s/120s/15s/2 次/30 分钟）。
func TestBotsRuntimeConfig(t *testing.T) {
	var zero BotsRuntimeConfig
	if got := zero.StreamFirstChunkTimeout(); got != 90*time.Second {
		t.Fatalf("首包超时默认 90s，got %v", got)
	}
	if got := zero.StreamIdleTimeout(); got != 120*time.Second {
		t.Fatalf("空闲超时默认 120s，got %v", got)
	}
	if got := zero.StopGrace(); got != 15*time.Second {
		t.Fatalf("停止宽限默认 15s，got %v", got)
	}
	if got := zero.RetryMaxAttemptsV(); got != 2 {
		t.Fatalf("重试次数默认 2，got %d", got)
	}
	if got := zero.RetryBaseDelay(); got != time.Second {
		t.Fatalf("退避基数默认 1s，got %v", got)
	}
	if got := zero.ProviderCircuitWindow(); got != 30*time.Minute {
		t.Fatalf("熔断窗口默认 30m，got %v", got)
	}
	if got := zero.PromptCacheRetain(); got != 10*time.Minute {
		t.Fatalf("前缀缓存窗口默认 10m，got %v", got)
	}
	// 显式 0 = 禁用推迟（窗口为零、入口始终激活）；与「未配置走默认」靠
	// 指针可区分（INT-139 云端复审）。
	disabled := 0
	if got := (BotsRuntimeConfig{PromptCacheRetainMinutes: &disabled}).PromptCacheRetain(); got != 0 {
		t.Fatalf("显式 0 应禁用推迟窗口，got %v", got)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := baseConfigYAML + "\nbots:\n  runtime:\n    stream_first_chunk_timeout_seconds: 5\n    retry_max_attempts: 9\n    provider_circuit_minutes: 1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load bots.runtime: %v", err)
	}
	if got := cfg.Bots.Runtime.StreamFirstChunkTimeout(); got != 5*time.Second {
		t.Fatalf("显式首包超时应生效，got %v", got)
	}
	if got := cfg.Bots.Runtime.RetryMaxAttemptsV(); got != 5 {
		t.Fatalf("重试次数应封顶 5，got %d", got)
	}
	if got := cfg.Bots.Runtime.ProviderCircuitWindow(); got != time.Minute {
		t.Fatalf("显式熔断窗口应生效，got %v", got)
	}

	negative := BotsRuntimeConfig{RetryMaxAttempts: -1, StopGraceSeconds: -1, StreamFirstChunkTimeoutSeconds: -1}
	if got := negative.RetryMaxAttemptsV(); got != 0 {
		t.Fatalf("负值应关闭重试，got %d", got)
	}
	if err := (BotsConfig{Runtime: negative}).validate(); err == nil {
		t.Fatalf("负的流超时应被 validate 拒绝")
	}
}
