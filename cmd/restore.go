package cmd

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/volcie/stash/internal/config"
	"github.com/volcie/stash/internal/restore"
)

func newRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore [service_name]",
		Short: "Restore service from backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if cfg == nil {
				return fmt.Errorf("configuration not loaded")
			}

			opts, err := parseRestoreFlags(cmd, args[0])
			if err != nil {
				return err
			}

			service, err := restore.NewService(cfg, noNotify)
			if err != nil {
				return fmt.Errorf("failed to initialize restore service: %w", err)
			}

			ctx := context.Background()
			return runRestore(ctx, service, opts)
		},
	}

	cmd.Flags().Bool("from-s3", false, "restore from S3 (default if no source specified)")
	cmd.Flags().String("from-local", "", "restore from local file")
	cmd.Flags().String("date", "", "specific backup date (YYYYMMDD or YYYYMMDD-HHMMSS)")
	cmd.Flags().Bool("latest", false, "use latest backup (default)")
	cmd.Flags().Bool("dry-run", false, "show what would be restored")
	cmd.Flags().Bool("force", false, "skip confirmation prompts")
	cmd.Flags().String("dest", "", "destination path (defaults to configured service path)")

	return cmd
}

func parseRestoreFlags(cmd *cobra.Command, serviceName string) (*restore.RestoreOptions, error) {
	fromS3, _ := cmd.Flags().GetBool("from-s3")
	fromLocal, _ := cmd.Flags().GetString("from-local")
	date, _ := cmd.Flags().GetString("date")
	latest, _ := cmd.Flags().GetBool("latest")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	destPath, _ := cmd.Flags().GetString("dest")

	// Default to S3 if no source specified
	if !fromS3 && fromLocal == "" {
		fromS3 = true
	}

	// Default to latest if no date specified
	if !latest && date == "" {
		latest = true
	}

	// Validate flags
	if fromS3 && fromLocal != "" {
		return nil, fmt.Errorf("cannot specify both --from-s3 and --from-local")
	}

	if fromLocal != "" && date != "" {
		return nil, fmt.Errorf("cannot specify --date when using --from-local")
	}

	return &restore.RestoreOptions{
		ServiceName: serviceName,
		FromS3:      fromS3,
		FromLocal:   fromLocal,
		Date:        date,
		Latest:      latest,
		DryRun:      dryRun,
		Force:       force,
		DestPath:    destPath,
	}, nil
}

func runRestore(ctx context.Context, service *restore.Service, opts *restore.RestoreOptions) error {
	if opts.FromLocal != "" {
		logrus.Infof("Starting restore from local file: %s", opts.FromLocal)
	} else {
		logrus.Infof("Starting restore for service: %s", opts.ServiceName)
	}

	if opts.DryRun {
		logrus.Info("DRY RUN MODE - No actual restore will be performed")
	}

	results, err := service.RestoreService(ctx, opts)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	return printRestoreResults(results, opts.DryRun)
}

func printRestoreResults(results []*restore.RestoreResult, dryRun bool) error {
	var totalSuccess, totalFailure int
	var hasErrors bool

	if dryRun {
		logrus.Info("=== Restore Preview (Dry Run) ===")
	} else {
		logrus.Info("=== Restore Results ===")
	}

	for _, result := range results {
		if result.Error != nil {
			logrus.WithFields(logrus.Fields{
				"service": result.Service,
				"path":    result.Path,
				"error":   result.Error,
			}).Error("Restore failed")
			totalFailure++
			hasErrors = true
		} else {
			fields := logrus.Fields{
				"service":      result.Service,
				"path":         result.Path,
				"restore_path": result.RestorePath,
			}

			if result.BackupInfo != nil {
				fields["backup_date"] = result.BackupInfo.Date.Format("2006-01-02 15:04:05")
				fields["source_key"] = result.BackupInfo.Key
			}

			if result.Duration > 0 {
				fields["duration"] = result.Duration
			}

			if dryRun {
				logrus.WithFields(fields).Info("Would restore backup")
			} else {
				logrus.WithFields(fields).Info("Restore completed successfully")
			}
			totalSuccess++
		}
	}

	if dryRun {
		logrus.WithFields(logrus.Fields{
			"would_restore": totalSuccess,
			"would_fail":    totalFailure,
		}).Info("Restore preview summary")
	} else {
		logrus.WithFields(logrus.Fields{
			"successful": totalSuccess,
			"failed":     totalFailure,
		}).Info("Restore summary")
	}

	if hasErrors {
		return fmt.Errorf("restore completed with %d failures", totalFailure)
	}

	return nil
}
