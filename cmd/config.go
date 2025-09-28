package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/volcie/stash/internal/config"
	"github.com/volcie/stash/internal/storage"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigEditCmd())
	cmd.AddCommand(newConfigInitCmd())
	cmd.AddCommand(newConfigTestCmd())

	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Display current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if cfg == nil {
				return fmt.Errorf("no configuration loaded")
			}

			// Config show output should go to stdout for user consumption
			logrus.Printf("Configuration: %s\n\n", getConfigPath())
			logrus.Printf("S3 Bucket: %s\n", cfg.S3.Bucket)
			logrus.Printf("S3 Prefix: %s\n", cfg.S3.Prefix)
			logrus.Printf("Retention: %d days\n", cfg.Retention)
			logrus.Printf("Services: %d\n", len(cfg.Services))

			for name, service := range cfg.Services {
				logrus.Printf("  %s (%d paths)\n", name, len(service.Paths))
			}

			return nil
		},
	}
}

func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open configuration file in editor",
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile := configPath
			if configFile == "" {
				configFile = "config.yaml"
			}

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "notepad"
			}

			editorCmd := exec.Command(editor, configFile)
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr

			return editorCmd.Run()
		},
	}
}

func newConfigInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration file with example values",
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile := configPath
			if configFile == "" {
				configFile = "config.yaml"
			}

			if _, err := os.Stat(configFile); err == nil {
				return fmt.Errorf("config file %s already exists", configFile)
			}

			exampleConfig := `# Stash Backup Configuration
#
# IMPORTANT: Set these environment variables before using stash:
#
# For AWS S3:
#   export AWS_ACCESS_KEY_ID=your-access-key
#   export AWS_SECRET_ACCESS_KEY=your-secret-key
#   export AWS_REGION=us-east-1
#
# For Cloudflare R2:
#   export AWS_ACCESS_KEY_ID=your-r2-token
#   export AWS_SECRET_ACCESS_KEY=your-r2-secret
#   export AWS_REGION=auto
#   export AWS_ENDPOINT_URL_S3=https://abc123.r2.cloudflarestorage.com
#
# For DigitalOcean Spaces:
#   export AWS_ACCESS_KEY_ID=your-spaces-key
#   export AWS_SECRET_ACCESS_KEY=your-spaces-secret
#   export AWS_REGION=nyc3
#   export AWS_ENDPOINT_URL_S3=https://nyc3.digitaloceanspaces.com
#
# Test your configuration with: stash config test

s3:
  bucket: your-s3-bucket-name
  prefix: backups

services:
  example-service:
    paths:
      data: /path/to/data
      logs: /path/to/logs
    include_folders:
      data:
        - important
        - configs

retention: 14
auto_cleanup: true

notifications:
  discord_webhook: 'https://discord.com/api/webhooks/...'
  on_success: true
  on_error: true
  on_warning: true

backup:
  temp_dir: /tmp/stash-backups
  preserve_acls: true
  compression: true
  min_size: 1024
`

			if err := os.WriteFile(configFile, []byte(exampleConfig), 0644); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}

			logrus.Infof("Configuration file created: %s", configFile)
			logrus.Info("Edit the file to match your environment and set up your S3 credentials")

			return nil
		},
	}
}

func newConfigTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Test S3 configuration and connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if cfg == nil {
				return fmt.Errorf("no configuration loaded")
			}

			// Config test output should go to stdout for user consumption
			logrus.Println("Testing S3 Configuration")
			logrus.Println("-------------------------")

			// Display environment variables (safely)
			logrus.Printf("AWS_ACCESS_KEY_ID: %s\n", getMaskedEnv("AWS_ACCESS_KEY_ID"))
			logrus.Printf("AWS_SECRET_ACCESS_KEY: %s\n", getMaskedEnv("AWS_SECRET_ACCESS_KEY"))
			logrus.Printf("AWS_REGION: %s\n", getEnvOrDefault("AWS_REGION", getEnvOrDefault("AWS_DEFAULT_REGION", "us-east-1")))

			endpoint := os.Getenv("AWS_ENDPOINT_URL_S3")
			if endpoint == "" {
				endpoint = os.Getenv("AWS_ENDPOINT_URL")
			}
			if endpoint != "" {
				logrus.Printf("Custom Endpoint: %s\n", endpoint)
			} else {
				logrus.Println("Custom Endpoint: (none - using AWS S3)")
			}

			logrus.Printf("S3 Bucket: %s\n", cfg.S3.Bucket)
			logrus.Printf("S3 Prefix: %s\n", cfg.S3.Prefix)

			logrus.Println("Testing Connection...")

			// Test S3 connectivity
			_, err := storage.NewS3Client(cfg.S3.Bucket, cfg.S3.Prefix)
			if err != nil {
				logrus.WithError(err).Error("S3 connection test failed")
				return fmt.Errorf("S3 connection test failed")
			}

			logrus.Info("S3 connection test successful")
			logrus.Println("Configuration is valid and S3 is accessible")

			return nil
		},
	}
}

func getMaskedEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		return "(not set)"
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getConfigPath() string {
	if configPath != "" {
		return configPath
	}
	return "config.yaml"
}
