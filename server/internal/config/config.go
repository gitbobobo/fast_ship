package config

import (
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Database   DatabaseConfig   `mapstructure:"database"`
	JWT        JWTConfig        `mapstructure:"jwt"`
	Upload     UploadConfig     `mapstructure:"upload"`
	Encryption EncryptionConfig `mapstructure:"encryption"`
	Issues     IssuesConfig     `mapstructure:"issues"`
	GitHubApp  GitHubAppConfig  `mapstructure:"github_app"`
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

// GitHubAppConfig GitHub App 身份（INT-93）。私钥只给 PEM 文件路径——私钥本体
// 不进库、不进环境变量正文；webhook_secret 为空时 webhook 端点 503。
type GitHubAppConfig struct {
	AppID          string `mapstructure:"app_id"`
	PrivateKeyPath string `mapstructure:"private_key_path"`
	WebhookSecret  string `mapstructure:"webhook_secret"`
	// Fake 仅 server.mode=debug 时允许 true：注入假 GitHub 实现，本地自测评审链路。
	Fake bool `mapstructure:"fake"`
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
	_ = viper.BindEnv("github_app.app_id", "FAST_SHIP_GITHUB_APP_APP_ID")
	_ = viper.BindEnv("github_app.private_key_path", "FAST_SHIP_GITHUB_APP_PRIVATE_KEY_PATH")
	_ = viper.BindEnv("github_app.webhook_secret", "FAST_SHIP_GITHUB_APP_WEBHOOK_SECRET")
	_ = viper.BindEnv("github_app.fake", "FAST_SHIP_GITHUB_APP_FAKE")

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
