package cmd

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/volcie/stash/internal/cleanup"
	"github.com/volcie/stash/internal/config"
	"github.com/volcie/stash/internal/storage"
	"github.com/volcie/stash/internal/utils"
)

func newCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up old backups based on retention policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if cfg == nil {
				return fmt.Errorf("configuration not loaded")
			}

			opts, err := parseCleanupFlags(cmd)
			if err != nil {
				return err
			}

			service, err := cleanup.NewService(cfg, noNotify)
			if err != nil {
				return fmt.Errorf("failed to initialize cleanup service: %w", err)
			}

			ctx := context.Background()
			return runCleanup(ctx, service, opts)
		},
	}

	cmd.Flags().String("service", "", "cleanup specific service only (or 'all' for all services)")
	cmd.Flags().Int("older-than", 0, "delete backups older than X days (uses config retention if not specified)")
	cmd.Flags().Bool("dry-run", false, "show what would be deleted without actually deleting")
	cmd.Flags().Int("keep-latest", 0, "always keep N latest backups per path")

	return cmd
}

func parseCleanupFlags(cmd *cobra.Command) (*cleanup.CleanupOptions, error) {
	serviceName, _ := cmd.Flags().GetString("service")
	olderThan, _ := cmd.Flags().GetInt("older-than")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	keepLatest, _ := cmd.Flags().GetInt("keep-latest")

	if olderThan < 0 {
		return nil, fmt.Errorf("older-than must be >= 0")
	}

	if keepLatest < 0 {
		return nil, fmt.Errorf("keep-latest must be >= 0")
	}

	return &cleanup.CleanupOptions{
		ServiceName: serviceName,
		OlderThan:   olderThan,
		DryRun:      dryRun,
		KeepLatest:  keepLatest,
	}, nil
}

func runCleanup(ctx context.Context, service *cleanup.Service, opts *cleanup.CleanupOptions) error {
	target := "all services"
	if opts.ServiceName != "" && opts.ServiceName != "all" {
		target = fmt.Sprintf("service: %s", opts.ServiceName)
	}

	logrus.Infof("Starting cleanup for %s", target)

	if opts.DryRun {
		logrus.Info("DRY RUN MODE - No actual deletion will be performed")
	}

	result, err := service.CleanupBackups(ctx, opts)
	if err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	return printCleanupResults(result, opts.DryRun)
}

func printCleanupResults(result *cleanup.CleanupResult, dryRun bool) error {
	deletedCount := len(result.DeletedBackups)

	if deletedCount == 0 {
		logrus.Info("No backups found for deletion")
		return nil
	}

	if dryRun {
		logrus.Info("=== Cleanup Preview (Dry Run) ===")
	} else {
		logrus.Info("=== Cleanup Results ===")
	}

	// Group by service for display
	serviceGroups := make(map[string]int)
	for _, backup := range result.DeletedBackups {
		serviceGroups[backup.Service]++
	}

	for serviceName, _ := range serviceGroups {
		var serviceBackups []*storage.BackupInfo
		var serviceSize int64

		for _, backup := range result.DeletedBackups {
			if backup.Service == serviceName {
				serviceBackups = append(serviceBackups, backup)
				serviceSize += backup.Size
			}
		}

		for _, backup := range serviceBackups {
			if dryRun {
				logrus.WithFields(logrus.Fields{
					"service": serviceName,
					"path":    backup.Path,
					"date":    backup.Date.Format("2006-01-02 15:04:05"),
					"size":    utils.FormatBytes(backup.Size),
					"key":     backup.Key,
				}).Info("Would delete backup")
			} else {
				logrus.WithFields(logrus.Fields{
					"service": serviceName,
					"path":    backup.Path,
					"date":    backup.Date.Format("2006-01-02 15:04:05"),
					"size":    utils.FormatBytes(backup.Size),
					"key":     backup.Key,
				}).Info("Deleted backup")
			}
		}

		logrus.WithFields(logrus.Fields{
			"service":      serviceName,
			"backup_count": len(serviceBackups),
			"total_size":   utils.FormatBytes(serviceSize),
		}).Infof("Service cleanup complete")
	}

	if dryRun {
		logrus.WithFields(logrus.Fields{
			"would_delete": deletedCount,
			"would_free":   utils.FormatBytes(result.TotalSize),
		}).Info("Cleanup preview summary")
	} else {
		logrus.WithFields(logrus.Fields{
			"deleted": deletedCount,
			"freed":   utils.FormatBytes(result.TotalSize),
		}).Info("Cleanup completed")
	}

	return nil
}
