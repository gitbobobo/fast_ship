package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Database   DatabaseConfig   `mapstructure:"database"`
	JWT        JWTConfig        `mapstructure:"jwt"`
	Upload     UploadConfig     `mapstructure:"upload"`
	Encryption EncryptionConfig `mapstructure:"encryption"`
	Issues     IssuesConfig     `mapstructure:"issues"`
	Logging    LoggingConfig    `mapstructure:"logging"`
	GitHubApp  GitHubAppConfig  `mapstructure:"github_app"`
	Bots       BotsConfig       `mapstructure:"bots"`
	Quota      QuotaConfig      `mapstructure:"quota"`
}

type ServerConfig struct {
	Port       int    `mapstructure:"port"`
	Mode       string `mapstructure:"mode"`
	WebDistDir string `mapstructure:"web_dist_dir"`
	// PublicURL 是外部可访问的服务基址（如 https://fs.example.com），
	// 用于自动化运行提示词中的回报端点（INT-90）。不从 Host 头推断。
	PublicURL string `mapstructure:"public_url"`
}

type DatabaseConfig struct {
	Path   string `mapstructure:"path"`
	LogSQL bool   `mapstructure:"log_sql"`
}

type JWTConfig struct {
	Secret             string `mapstructure:"secret"`
	ExpireHours        int    `mapstructure:"expire_hours"`
	RefreshExpireHours int    `mapstructure:"refresh_expire_hours"`
}

type UploadConfig struct {
	MaxFileSize int64  `mapstructure:"max_file_size"`
	StoragePath string `mapstructure:"storage_path"`
}

type EncryptionConfig struct {
	Key string `mapstructure:"key"`
}

type IssuesConfig struct {
	AutoSyncEnabled         bool `mapstructure:"auto_sync_enabled"`
	AutoSyncOnStartup       bool `mapstructure:"auto_sync_on_startup"`
	AutoSyncIntervalMinutes int  `mapstructure:"auto_sync_interval_minutes"`
}

// LoggingConfig 是 Fast Ship 自身日志（INT-94）的采集配置。
type LoggingConfig struct {
	// Path 是独立 SQLite 日志库文件路径；空串回退 data/self_logs.db。
	Path string `mapstructure:"self_log_path"`
	// RetentionDays 是保留天数；≤0 取默认 7。
	RetentionDays int `mapstructure:"retention_days"`
	// MaxEntries 是最大条数上限；≤0 取默认 100 万。
	MaxEntries int64 `mapstructure:"max_entries"`
	// CleanupIntervalMinutes 是清理周期；≤0 取默认 60 分钟。
	CleanupIntervalMinutes int `mapstructure:"cleanup_interval_minutes"`
	// MinLevel 是入库最低级别（debug/info/warn/error）；stdout 不受影响。
	MinLevel string `mapstructure:"min_level"`
}

// SelfLogPath 返回日志库文件路径（含默认回退）。
func (c LoggingConfig) SelfLogPath() string {
	if c.Path == "" {
		return "./data/self_logs.db"
	}
	return c.Path
}

// SelfLogRetentionDays 返回保留天数（含默认回退）。
func (c LoggingConfig) SelfLogRetentionDays() int {
	if c.RetentionDays <= 0 {
		return 7
	}
	return c.RetentionDays
}

// SelfLogMaxEntries 返回最大条数上限（含默认回退）。
func (c LoggingConfig) SelfLogMaxEntries() int64 {
	if c.MaxEntries <= 0 {
		return 1000000
	}
	return c.MaxEntries
}

// SelfLogCleanupInterval 返回清理周期（含默认回退 1 小时）。
func (c LoggingConfig) SelfLogCleanupInterval() time.Duration {
	if c.CleanupIntervalMinutes <= 0 {
		return time.Hour
	}
	return time.Duration(c.CleanupIntervalMinutes) * time.Minute
}

// GitHubAppConfig GitHub App 身份（INT-93；INT-129 起为部署级兜底）。私钥只给
// PEM 文件路径——私钥本体不进库、不进环境变量正文；webhook_secret 为空且部署
// 设置行也没有密钥时 webhook 端点 503。
type GitHubAppConfig struct {
	AppID          string `mapstructure:"app_id"`
	PrivateKeyPath string `mapstructure:"private_key_path"`
	WebhookSecret  string `mapstructure:"webhook_secret"`
	// APIBaseURL GitHub API base；留空取官方 https://api.github.com。
	// 本地端到端可指向 mock GitHub 服务。
	APIBaseURL string `mapstructure:"api_base_url"`
	// Fake 仅 server.mode=debug 时允许 true：注入假 GitHub 实现，本地自测评审链路。
	Fake bool `mapstructure:"fake"`
}

// BotsConfig 是机器人（INT-101/INT-102）的部署配置：上下文压缩阈值、记忆
// 限额与夜间整理时程。全部为部署级配置，无机器人管理表单字段。
type BotsConfig struct {
	Context BotsContextConfig `mapstructure:"context"`
	Memory  BotsMemoryConfig  `mapstructure:"memory"`
	Runtime BotsRuntimeConfig `mapstructure:"runtime"`
}

// BotsRuntimeConfig 是模型请求的运行参数（INT-137）：流式看门狗、停止宽限与
// 未输出前的有限重试。全部为部署级配置。
type BotsRuntimeConfig struct {
	// StreamFirstChunkTimeoutSeconds 是发出请求到第一个流式片段的超时（默认 90s）。
	StreamFirstChunkTimeoutSeconds int `mapstructure:"stream_first_chunk_timeout_seconds"`
	// StreamIdleTimeoutSeconds 是相邻流式片段之间的空闲超时（默认 120s）。
	StreamIdleTimeoutSeconds int `mapstructure:"stream_idle_timeout_seconds"`
	// StopGraceSeconds 是「停止」消息受理后等待 run 自然到达安全点的宽限，
	// 超时即硬取消当前模型请求（默认 15s）。
	StopGraceSeconds int `mapstructure:"stop_grace_seconds"`
	// RetryMaxAttempts 是未收到任何片段就失败时的最大重试次数（默认 2）。
	RetryMaxAttempts int `mapstructure:"retry_max_attempts"`
	// RetryBaseDelayMs 是重试指数退避的基数（默认 1000ms）。
	RetryBaseDelayMs int `mapstructure:"retry_base_delay_ms"`
	// ProviderCircuitMinutes 是 provider 熔断窗口（默认 30 分钟）：认证失败/
	// 额度用完时定时任务在该窗口内直接记 skipped。
	ProviderCircuitMinutes int `mapstructure:"provider_circuit_minutes"`
	// PromptCacheRetainMinutes 是前缀缓存在上游的保留窗口（默认 10 分钟，
	// INT-139）：主线 run 入口发现版本落后时，最近一次主线模型请求仍在窗口
	// 内就不在入口激活（重写前缀会作废仍热的缓存），推迟到下一个压缩边界。
	PromptCacheRetainMinutes int `mapstructure:"prompt_cache_retain_minutes"`
}

func (c BotsRuntimeConfig) StreamFirstChunkTimeout() time.Duration {
	if c.StreamFirstChunkTimeoutSeconds > 0 {
		return time.Duration(c.StreamFirstChunkTimeoutSeconds) * time.Second
	}
	return 90 * time.Second
}

func (c BotsRuntimeConfig) StreamIdleTimeout() time.Duration {
	if c.StreamIdleTimeoutSeconds > 0 {
		return time.Duration(c.StreamIdleTimeoutSeconds) * time.Second
	}
	return 120 * time.Second
}

func (c BotsRuntimeConfig) StopGrace() time.Duration {
	if c.StopGraceSeconds > 0 {
		return time.Duration(c.StopGraceSeconds) * time.Second
	}
	return 15 * time.Second
}

// RetryMaxAttemptsV 是「首次失败之后」的重试次数：未配置取默认 2，负值
// 关闭重试，上限 5。
func (c BotsRuntimeConfig) RetryMaxAttemptsV() int {
	if c.RetryMaxAttempts < 0 {
		return 0
	}
	if c.RetryMaxAttempts > 5 {
		return 5
	}
	if c.RetryMaxAttempts == 0 {
		return 2
	}
	return c.RetryMaxAttempts
}

func (c BotsRuntimeConfig) RetryBaseDelay() time.Duration {
	if c.RetryBaseDelayMs > 0 {
		return time.Duration(c.RetryBaseDelayMs) * time.Millisecond
	}
	return time.Second
}

// ProviderCircuitWindow 是熔断窗口：认证失败/额度用完的 provider 在窗口内
// 不派发定时任务，避免每次到点都失败一次。
func (c BotsRuntimeConfig) ProviderCircuitWindow() time.Duration {
	if c.ProviderCircuitMinutes > 0 {
		return time.Duration(c.ProviderCircuitMinutes) * time.Minute
	}
	return 30 * time.Minute
}

// PromptCacheRetain 是「最近一次主线请求距今多久算前缀缓存已冷」的窗口；
// 未配置取默认 10 分钟。窗口内推迟入口激活，让活跃对话保住前缀命中。
func (c BotsRuntimeConfig) PromptCacheRetain() time.Duration {
	if c.PromptCacheRetainMinutes > 0 {
		return time.Duration(c.PromptCacheRetainMinutes) * time.Minute
	}
	return 10 * time.Minute
}

type BotsContextConfig struct {
	// WindowTokens 是默认模型上下文窗口；ModelWindows 按实际模型名覆盖。
	WindowTokens int64            `mapstructure:"window_tokens"`
	ModelWindows map[string]int64 `mapstructure:"model_windows"`
	// CompressionRatio 触发压缩的窗口占比（默认 0.75）。
	CompressionRatio float64 `mapstructure:"compression_ratio"`
	// TargetRatio 压缩后目标体量占触发阈值的比例（默认 0.45）。
	TargetRatio float64 `mapstructure:"target_ratio"`
	// ProtectUserTurns 保护最近的用户轮数（最小 3）。
	ProtectUserTurns int `mapstructure:"protect_user_turns"`
	// FoldLargeRatio 判定「大结果」的窗口占比（默认 0.01）。
	FoldLargeRatio float64 `mapstructure:"fold_large_ratio"`
	// FoldMinClearRatio 折叠批量发布的最小净清除量占比（默认 0.02）。
	FoldMinClearRatio float64 `mapstructure:"fold_min_clear_ratio"`
	// SummaryModel / ArchiveModel 空值继承机器人主模型。
	SummaryModel string `mapstructure:"summary_model"`
	ArchiveModel string `mapstructure:"archive_model"`
}

type BotsMemoryConfig struct {
	// UserMaxChars / MemoryMaxChars 是常驻文件的注入字符上限（Unicode 字符）。
	UserMaxChars   int64 `mapstructure:"user_max_chars"`
	MemoryMaxChars int64 `mapstructure:"memory_max_chars"`
	// NightlyModel 空值继承机器人主模型。
	NightlyModel string `mapstructure:"nightly_model"`
	// Timezone 决定夜间整理的「当地日期」与 daily 文件命名（IANA 时区）。
	Timezone string `mapstructure:"timezone"`
	// NightlyTime 是当地时区的每日触发时刻（HH:MM，默认 03:00）。
	NightlyTime string `mapstructure:"nightly_time"`
}

// WindowTokensFor 返回指定模型的上下文窗口（未知模型用默认值）。
func (c BotsContextConfig) WindowTokensFor(model string) int64 {
	if c.ModelWindows != nil {
		if w, ok := c.ModelWindows[model]; ok && w > 0 {
			return w
		}
	}
	if c.WindowTokens > 0 {
		return c.WindowTokens
	}
	return 32768
}

func (c BotsContextConfig) CompressionRatioV() float64 {
	if c.CompressionRatio > 0 && c.CompressionRatio < 1 {
		return c.CompressionRatio
	}
	return 0.75
}

func (c BotsContextConfig) TargetRatioV() float64 {
	if c.TargetRatio > 0 && c.TargetRatio < 1 {
		return c.TargetRatio
	}
	return 0.45
}

func (c BotsContextConfig) ProtectUserTurnsV() int {
	if c.ProtectUserTurns >= 3 {
		return c.ProtectUserTurns
	}
	return 3
}

func (c BotsContextConfig) FoldLargeRatioV() float64 {
	if c.FoldLargeRatio > 0 && c.FoldLargeRatio < 1 {
		return c.FoldLargeRatio
	}
	return 0.01
}

func (c BotsContextConfig) FoldMinClearRatioV() float64 {
	if c.FoldMinClearRatio > 0 && c.FoldMinClearRatio < 1 {
		return c.FoldMinClearRatio
	}
	return 0.02
}

// botMemoryInjectMinChars 是注入上限下限：必须能放下截断标记本身。
const botMemoryInjectMinChars int64 = 64

func (c BotsMemoryConfig) UserMaxCharsV() int64 {
	if c.UserMaxChars > 0 {
		return c.UserMaxChars
	}
	return 1500
}

func (c BotsMemoryConfig) MemoryMaxCharsV() int64 {
	if c.MemoryMaxChars > 0 {
		return c.MemoryMaxChars
	}
	return 8000
}

// Location 解析夜间整理时区；非法值回退 UTC（Load 已校验，这里是防御）。
func (c BotsMemoryConfig) Location() *time.Location {
	if c.Timezone != "" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// NightlyClock 返回每日触发时刻（时、分）；非法值回退 03:00。
func (c BotsMemoryConfig) NightlyClock() (hour, minute int) {
	var h, m int
	if _, err := fmt.Sscanf(c.NightlyTime, "%d:%d", &h, &m); err == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59 {
		return h, m
	}
	return 3, 0
}

// QuotaConfig 是订阅套餐额度（INT-109）的抓取与调度配置。
// BaseURLs 按供应商 id 覆盖默认 origin，仅部署/测试用。
type QuotaConfig struct {
	Enabled                      bool              `mapstructure:"enabled"`
	IntervalMinutes              int               `mapstructure:"interval_minutes"`
	RunOnStartup                 bool              `mapstructure:"run_on_startup"`
	MaxConcurrency               int               `mapstructure:"max_concurrency"`
	RequestTimeoutSeconds        int               `mapstructure:"request_timeout_seconds"`
	ManualRefreshCooldownSeconds int               `mapstructure:"manual_refresh_cooldown_seconds"`
	BaseURLs                     map[string]string `mapstructure:"base_urls"`
}

// Interval 返回调度轮间隔（默认 10 分钟）。
func (c QuotaConfig) Interval() time.Duration {
	if c.IntervalMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(c.IntervalMinutes) * time.Minute
}

// MaxConcurrencyV 返回单轮并发抓取上限（默认 3）。
func (c QuotaConfig) MaxConcurrencyV() int {
	if c.MaxConcurrency <= 0 {
		return 3
	}
	return c.MaxConcurrency
}

// RequestTimeout 返回单次抓取超时（默认 20 秒）。
func (c QuotaConfig) RequestTimeout() time.Duration {
	if c.RequestTimeoutSeconds <= 0 {
		return 20 * time.Second
	}
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}

// ManualRefreshCooldown 返回手动刷新冷却（默认 30 秒）。
func (c QuotaConfig) ManualRefreshCooldown() time.Duration {
	if c.ManualRefreshCooldownSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.ManualRefreshCooldownSeconds) * time.Second
}

// validate 配置级校验：明显非法值直接拒绝启动。
func (c BotsConfig) validate() error {
	if c.Context.CompressionRatio != 0 && (c.Context.CompressionRatio <= 0 || c.Context.CompressionRatio >= 1) {
		return fmt.Errorf("bots.context.compression_ratio 必须在 (0,1) 内")
	}
	if c.Context.TargetRatio != 0 && (c.Context.TargetRatio <= 0 || c.Context.TargetRatio >= 1) {
		return fmt.Errorf("bots.context.target_ratio 必须在 (0,1) 内")
	}
	if c.Context.ProtectUserTurns != 0 && c.Context.ProtectUserTurns < 3 {
		return fmt.Errorf("bots.context.protect_user_turns 不得小于 3")
	}
	if c.Memory.UserMaxChars != 0 && c.Memory.UserMaxChars < botMemoryInjectMinChars {
		return fmt.Errorf("bots.memory.user_max_chars 不得小于 %d（需容纳截断标记）", botMemoryInjectMinChars)
	}
	if c.Memory.MemoryMaxChars != 0 && c.Memory.MemoryMaxChars < botMemoryInjectMinChars {
		return fmt.Errorf("bots.memory.memory_max_chars 不得小于 %d（需容纳截断标记）", botMemoryInjectMinChars)
	}
	if c.Runtime.StreamFirstChunkTimeoutSeconds < 0 || c.Runtime.StreamIdleTimeoutSeconds < 0 {
		return fmt.Errorf("bots.runtime 的流式超时不得为负")
	}
	if c.Runtime.StopGraceSeconds < 0 {
		return fmt.Errorf("bots.runtime.stop_grace_seconds 不得为负")
	}
	if c.Runtime.PromptCacheRetainMinutes < 0 {
		return fmt.Errorf("bots.runtime.prompt_cache_retain_minutes 不得为负")
	}
	if c.Memory.Timezone != "" {
		if _, err := time.LoadLocation(c.Memory.Timezone); err != nil {
			return fmt.Errorf("bots.memory.timezone 非法: %v", err)
		}
	}
	if c.Memory.NightlyTime != "" {
		var h, m int
		if _, err := fmt.Sscanf(c.Memory.NightlyTime, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return fmt.Errorf("bots.memory.nightly_time 必须是 HH:MM")
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	viper.SetConfigFile(path)

	// 新配置段的缺省值：老部署的配置文件里没有 quota 段时，viper 会把这两个
	// bool 反序列化成 false，等于升级后静默关掉后台抓取；这里给缺省值兜底，
	// 显式写 false 仍可关闭。其余数值项走 QuotaConfig 上的默认回退方法。
	viper.SetDefault("quota.enabled", true)
	viper.SetDefault("quota.run_on_startup", true)

	// 支持环境变量覆盖
	viper.SetEnvPrefix("FAST_SHIP")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// 绑定关键环境变量
	_ = viper.BindEnv("server.mode", "FAST_SHIP_SERVER_MODE")
	_ = viper.BindEnv("database.log_sql", "FAST_SHIP_DATABASE_LOG_SQL")
	_ = viper.BindEnv("jwt.secret", "JWT_SECRET")
	_ = viper.BindEnv("encryption.key", "ENCRYPTION_KEY")
	_ = viper.BindEnv("server.web_dist_dir", "FAST_SHIP_WEB_DIST_DIR")
	_ = viper.BindEnv("server.public_url", "FAST_SHIP_SERVER_PUBLIC_URL")
	_ = viper.BindEnv("logging.self_log_path", "FAST_SHIP_LOGGING_SELF_LOG_PATH")
	_ = viper.BindEnv("logging.min_level", "FAST_SHIP_LOGGING_MIN_LEVEL")
	_ = viper.BindEnv("github_app.app_id", "FAST_SHIP_GITHUB_APP_APP_ID")
	_ = viper.BindEnv("github_app.private_key_path", "FAST_SHIP_GITHUB_APP_PRIVATE_KEY_PATH")
	_ = viper.BindEnv("github_app.webhook_secret", "FAST_SHIP_GITHUB_APP_WEBHOOK_SECRET")
	_ = viper.BindEnv("github_app.api_base_url", "FAST_SHIP_GITHUB_APP_API_BASE_URL")
	_ = viper.BindEnv("github_app.fake", "FAST_SHIP_GITHUB_APP_FAKE")
	_ = viper.BindEnv("bots.context.window_tokens", "FAST_SHIP_BOTS_CONTEXT_WINDOW_TOKENS")
	_ = viper.BindEnv("bots.context.compression_ratio", "FAST_SHIP_BOTS_CONTEXT_COMPRESSION_RATIO")
	_ = viper.BindEnv("bots.context.target_ratio", "FAST_SHIP_BOTS_CONTEXT_TARGET_RATIO")
	_ = viper.BindEnv("bots.context.protect_user_turns", "FAST_SHIP_BOTS_CONTEXT_PROTECT_USER_TURNS")
	_ = viper.BindEnv("bots.context.fold_large_ratio", "FAST_SHIP_BOTS_CONTEXT_FOLD_LARGE_RATIO")
	_ = viper.BindEnv("bots.context.fold_min_clear_ratio", "FAST_SHIP_BOTS_CONTEXT_FOLD_MIN_CLEAR_RATIO")
	_ = viper.BindEnv("bots.context.summary_model", "FAST_SHIP_BOTS_CONTEXT_SUMMARY_MODEL")
	_ = viper.BindEnv("bots.context.archive_model", "FAST_SHIP_BOTS_CONTEXT_ARCHIVE_MODEL")
	_ = viper.BindEnv("bots.memory.user_max_chars", "FAST_SHIP_BOTS_MEMORY_USER_MAX_CHARS")
	_ = viper.BindEnv("bots.memory.memory_max_chars", "FAST_SHIP_BOTS_MEMORY_MEMORY_MAX_CHARS")
	_ = viper.BindEnv("bots.memory.nightly_model", "FAST_SHIP_BOTS_MEMORY_NIGHTLY_MODEL")
	_ = viper.BindEnv("bots.memory.timezone", "FAST_SHIP_BOTS_MEMORY_TIMEZONE")
	_ = viper.BindEnv("bots.memory.nightly_time", "FAST_SHIP_BOTS_MEMORY_NIGHTLY_TIME")
	// INT-137 运行参数：与上面的 bot 配置同口径，允许部署用环境变量覆盖。
	_ = viper.BindEnv("bots.runtime.stream_first_chunk_timeout_seconds", "FAST_SHIP_BOTS_RUNTIME_STREAM_FIRST_CHUNK_TIMEOUT_SECONDS")
	_ = viper.BindEnv("bots.runtime.stream_idle_timeout_seconds", "FAST_SHIP_BOTS_RUNTIME_STREAM_IDLE_TIMEOUT_SECONDS")
	_ = viper.BindEnv("bots.runtime.stop_grace_seconds", "FAST_SHIP_BOTS_RUNTIME_STOP_GRACE_SECONDS")
	_ = viper.BindEnv("bots.runtime.retry_max_attempts", "FAST_SHIP_BOTS_RUNTIME_RETRY_MAX_ATTEMPTS")
	_ = viper.BindEnv("bots.runtime.retry_base_delay_ms", "FAST_SHIP_BOTS_RUNTIME_RETRY_BASE_DELAY_MS")
	_ = viper.BindEnv("bots.runtime.provider_circuit_minutes", "FAST_SHIP_BOTS_RUNTIME_PROVIDER_CIRCUIT_MINUTES")
	_ = viper.BindEnv("bots.runtime.prompt_cache_retain_minutes", "FAST_SHIP_BOTS_RUNTIME_PROMPT_CACHE_RETAIN_MINUTES")
	_ = viper.BindEnv("quota.enabled", "FAST_SHIP_QUOTA_ENABLED")
	_ = viper.BindEnv("quota.interval_minutes", "FAST_SHIP_QUOTA_INTERVAL_MINUTES")
	_ = viper.BindEnv("quota.run_on_startup", "FAST_SHIP_QUOTA_RUN_ON_STARTUP")
	_ = viper.BindEnv("quota.max_concurrency", "FAST_SHIP_QUOTA_MAX_CONCURRENCY")
	_ = viper.BindEnv("quota.request_timeout_seconds", "FAST_SHIP_QUOTA_REQUEST_TIMEOUT_SECONDS")
	_ = viper.BindEnv("quota.manual_refresh_cooldown_seconds", "FAST_SHIP_QUOTA_MANUAL_REFRESH_COOLDOWN_SECONDS")

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Bots.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
