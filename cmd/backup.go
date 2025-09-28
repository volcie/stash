package cmd

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/volcie/stash/internal/backup"
	"github.com/volcie/stash/internal/config"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup [service_name|all]",
		Short: "Backup services to S3",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if cfg == nil {
				return fmt.Errorf("configuration not loaded")
			}

			paths, _ := cmd.Flags().GetStringSlice("paths")

			service, err := backup.NewService(cfg, noNotify)
			if err != nil {
				return fmt.Errorf("failed to initialize backup service: %w", err)
			}

			ctx := context.Background()

			if len(args) == 0 || args[0] == "all" {
				return runBackupAll(ctx, service, paths)
			} else {
				return runBackupService(ctx, service, args[0], paths)
			}
		},
	}

	cmd.Flags().StringSlice("paths", nil, "backup only these paths (comma-separated)")

	return cmd
}

func runBackupService(ctx context.Context, service *backup.Service, serviceName string, paths []string) error {
	logrus.Infof("Starting backup for service: %s", serviceName)

	results, err := service.BackupService(ctx, serviceName, paths)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	return printBackupResults(map[string][]*backup.BackupResult{serviceName: results})
}

func runBackupAll(ctx context.Context, service *backup.Service, paths []string) error {
	logrus.Info("Starting backup for all services")

	allResults, err := service.BackupAll(ctx, paths)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	return printBackupResults(allResults)
}

func printBackupResults(allResults map[string][]*backup.BackupResult) error {
	var totalSuccess, totalFailure int
	var hasErrors bool

	logrus.Info("=== Backup Results ===")

	for serviceName, results := range allResults {
		logrus.Infof("Service: %s", serviceName)

		for _, result := range results {
			if result.Error != nil {
				logrus.WithFields(logrus.Fields{
					"service": serviceName,
					"path":    result.Path,
					"error":   result.Error,
				}).Error("Backup failed")
				totalFailure++
				hasErrors = true
			} else {
				logrus.WithFields(logrus.Fields{
					"service":  serviceName,
					"path":     result.Path,
					"s3_key":   result.BackupInfo.Key,
					"size_mb":  fmt.Sprintf("%.2f", float64(result.ArchiveSize)/1024/1024),
					"duration": result.Duration,
				}).Info("Backup completed successfully")
				totalSuccess++
			}
		}
	}

	logrus.WithFields(logrus.Fields{
		"successful": totalSuccess,
		"failed":     totalFailure,
	}).Info("Backup summary")

	if hasErrors {
		return fmt.Errorf("backup completed with %d failures", totalFailure)
	}

	return nil
}
