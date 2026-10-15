package config

import (
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

func Load(path string) (*Config, error) {
	viper.SetConfigFile(path)

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

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
