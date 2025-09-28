package config

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/viper"
)

type Config struct {
	S3            S3Config           `mapstructure:"s3"`
	Services      map[string]Service `mapstructure:"services"`
	Retention     int                `mapstructure:"retention"`
	Notifications NotificationConfig `mapstructure:"notifications"`
	Backup        BackupConfig       `mapstructure:"backup"`
}

type S3Config struct {
	Bucket string `mapstructure:"bucket"`
	Prefix string `mapstructure:"prefix"`
}

type Service struct {
	Paths          map[string]string   `mapstructure:"paths"`
	IncludeFolders map[string][]string `mapstructure:"include_folders"`
}

type NotificationConfig struct {
	DiscordWebhook string `mapstructure:"discord_webhook"`
	OnSuccess      bool   `mapstructure:"on_success"`
	OnError        bool   `mapstructure:"on_error"`
	OnWarning      bool   `mapstructure:"on_warning"`
}

type BackupConfig struct {
	TempDir      string `mapstructure:"temp_dir"`
	PreserveACLs bool   `mapstructure:"preserve_acls"`
	Compression  bool   `mapstructure:"compression"`
	MinSize      int64  `mapstructure:"min_size"`
}

var globalConfig *Config

func Load(configPath string) (*Config, error) {
	v := viper.New()

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	globalConfig = &config
	return &config, nil
}

func Get() *Config {
	return globalConfig
}

func validateConfig(cfg *Config) error {
	if cfg.S3.Bucket == "" {
		return fmt.Errorf("s3.bucket is required")
	}

	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service must be configured")
	}

	for name, service := range cfg.Services {
		if len(service.Paths) == 0 {
			return fmt.Errorf("service %s must have at least one path configured", name)
		}

		for pathName, path := range service.Paths {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("service %s path %s must be an absolute path", name, pathName)
			}
		}
	}

	if cfg.Retention <= 0 {
		return fmt.Errorf("retention must be greater than 0")
	}

	if cfg.Backup.MinSize < 0 {
		return fmt.Errorf("backup.min_size cannot be negative")
	}

	return nil
}
