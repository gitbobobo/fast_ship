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
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type DatabaseConfig struct {
	Path string `mapstructure:"path"`
}

type JWTConfig struct {
	Secret      string `mapstructure:"secret"`
	ExpireHours int    `mapstructure:"expire_hours"`
}

type UploadConfig struct {
	MaxFileSize int64  `mapstructure:"max_file_size"`
	StoragePath string `mapstructure:"storage_path"`
}

type EncryptionConfig struct {
	Key string `mapstructure:"key"`
}

func Load(path string) (*Config, error) {
	viper.SetConfigFile(path)

	// 支持环境变量覆盖
	viper.SetEnvPrefix("FAST_SHIP")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// 绑定关键环境变量
	_ = viper.BindEnv("jwt.secret", "JWT_SECRET")
	_ = viper.BindEnv("encryption.key", "ENCRYPTION_KEY")

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
