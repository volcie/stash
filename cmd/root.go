package cmd

import (
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/volcie/stash/internal/config"
)

var (
	configPath string
	verbose    bool
	noNotify   bool
	version    = "dev" // set via ldflags during build
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "stash",
		Short:        "Backup utilities for revolt-rp.net",
		Long:         "A CLI tool for backing up files to S3 with Discord notifications",
		Version:      version,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			logrus.SetFormatter(&logrus.TextFormatter{ForceColors: true, DisableTimestamp: true})

			if verbose {
				logrus.SetLevel(logrus.DebugLevel)
			}

			// Skip config loading for config init command
			if cmd.Use == "init" {
				return nil
			}

			if _, err := config.Load(configPath); err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.PersistentFlags().StringVar(&configPath, "config", "", "config file path")
	cmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "verbose output")
	cmd.PersistentFlags().BoolVar(&noNotify, "no-notify", false, "skip Discord notifications")
	cmd.SetVersionTemplate("stash version {{.Version}}\n")

	cmd.AddCommand(newBackupCmd())
	cmd.AddCommand(newRestoreCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newCleanupCmd())
	cmd.AddCommand(newConfigCmd())

	return cmd
}

func Execute() error {
	if err := newRootCmd().Execute(); err != nil {
		return fmt.Errorf("error executing root command: %w", err)
	}

	return nil
}
