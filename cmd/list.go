package cmd

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/volcie/stash/internal/config"
	"github.com/volcie/stash/internal/storage"
	"github.com/volcie/stash/internal/utils"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available backups",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if cfg == nil {
				return fmt.Errorf("configuration not loaded")
			}

			serviceName, _ := cmd.Flags().GetString("service")
			s3Flag, _ := cmd.Flags().GetBool("s3")
			localFlag, _ := cmd.Flags().GetBool("local")

			// Default to S3 if neither specified
			if !s3Flag && !localFlag {
				s3Flag = true
			}

			if s3Flag {
				return listS3Backups(cfg, serviceName)
			}

			if localFlag {
				return fmt.Errorf("local backup listing not available yet")
			}

			return nil
		},
	}

	cmd.Flags().String("service", "", "filter by service name")
	cmd.Flags().Bool("local", false, "list local backups")
	cmd.Flags().Bool("s3", false, "list S3 backups (default if no flags specified)")

	return cmd
}

func listS3Backups(cfg *config.Config, serviceName string) error {
	s3Client, err := storage.NewS3Client(cfg.S3.Bucket, cfg.S3.Prefix)
	if err != nil {
		return fmt.Errorf("failed to create S3 client: %w", err)
	}

	ctx := context.Background()
	backups, err := s3Client.List(ctx, serviceName)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	if len(backups) == 0 {
		if serviceName != "" {
			logrus.Infof("No backups found for service: %s", serviceName)
		} else {
			logrus.Info("No backups found")
		}
		return nil
	}

	// Sort by date (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Date.After(backups[j].Date)
	})

	// Group by service
	serviceGroups := make(map[string][]*storage.BackupInfo)
	for _, backup := range backups {
		serviceGroups[backup.Service] = append(serviceGroups[backup.Service], backup)
	}

	// List command output should go to stdout for user consumption
	logrus.Printf("S3 Backups (s3://%s/%s)\n\n", cfg.S3.Bucket, cfg.S3.Prefix)

	for service, serviceBackups := range serviceGroups {
		logrus.Printf("%s (%d backups)\n", service, len(serviceBackups))

		for _, backup := range serviceBackups {
			age := time.Since(backup.Date)
			logrus.Printf("  %s | %s | %s | %s ago\n",
				backup.Path,
				backup.Date.Format("2006-01-02 15:04"),
				utils.FormatBytes(backup.Size),
				formatDuration(age))
		}
		logrus.Println()
	}

	logrus.WithField("total", len(backups)).Debug("Listed backups")
	return nil
}

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.0fd", d.Hours()/24)
}

