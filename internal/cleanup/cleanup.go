package cleanup

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/volcie/stash/internal/config"
	"github.com/volcie/stash/internal/notifications"
	"github.com/volcie/stash/internal/storage"
)

type Service struct {
	cfg      *config.Config
	s3Client *storage.S3Client
	notifier *notifications.DiscordNotifier
}

type CleanupOptions struct {
	ServiceName string
	OlderThan   int
	DryRun      bool
	KeepLatest  int
}

type CleanupResult struct {
	DeletedBackups []*storage.BackupInfo
	TotalSize      int64
	Error          error
}

func NewService(cfg *config.Config, noNotify bool) (*Service, error) {
	s3Client, err := storage.NewS3Client(cfg.S3.Bucket, cfg.S3.Prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}

	var notifier *notifications.DiscordNotifier
	if !noNotify && cfg.Notifications.DiscordWebhook != "" {
		notifier = notifications.NewDiscordNotifier(
			cfg.Notifications.DiscordWebhook,
			cfg.Notifications.OnSuccess,
			cfg.Notifications.OnError,
			cfg.Notifications.OnWarning,
		)
	}

	return &Service{
		cfg:      cfg,
		s3Client: s3Client,
		notifier: notifier,
	}, nil
}

func (s *Service) CleanupBackups(ctx context.Context, opts *CleanupOptions) (*CleanupResult, error) {
	result := &CleanupResult{}

	// Use retention from config if not specified
	olderThan := opts.OlderThan
	if olderThan == 0 {
		olderThan = s.cfg.Retention
	}

	if olderThan <= 0 {
		return nil, fmt.Errorf("retention period must be greater than 0")
	}

	logrus.Infof("Starting cleanup: older than %d days, keep latest %d", olderThan, opts.KeepLatest)

	var servicesToClean []string
	if opts.ServiceName == "" || opts.ServiceName == "all" {
		// Clean all services
		for serviceName := range s.cfg.Services {
			servicesToClean = append(servicesToClean, serviceName)
		}
	} else {
		// Clean specific service
		if _, exists := s.cfg.Services[opts.ServiceName]; !exists {
			return nil, fmt.Errorf("service %s not found in configuration", opts.ServiceName)
		}
		servicesToClean = []string{opts.ServiceName}
	}

	var allDeletedBackups []*storage.BackupInfo
	var totalSize int64

	for _, serviceName := range servicesToClean {
		logrus.Infof("Cleaning up service: %s", serviceName)

		backups, err := s.s3Client.List(ctx, serviceName)
		if err != nil {
			logrus.Errorf("Failed to list backups for service %s: %v", serviceName, err)
			continue
		}

		toDelete := s.selectBackupsForDeletion(backups, olderThan, opts.KeepLatest)
		if len(toDelete) == 0 {
			logrus.Infof("No backups to delete for service %s", serviceName)
			continue
		}

		logrus.Infof("Found %d backups to delete for service %s", len(toDelete), serviceName)

		if opts.DryRun {
			logrus.Info("DRY RUN: Would delete the following backups:")
			for _, backup := range toDelete {
				logrus.Infof("  - %s (%s, %s)", backup.Key, backup.Date.Format("2006-01-02 15:04:05"), formatBytes(backup.Size))
				totalSize += backup.Size
			}
			allDeletedBackups = append(allDeletedBackups, toDelete...)
			continue
		}

		// Delete backups
		keys := make([]string, len(toDelete))
		for i, backup := range toDelete {
			keys[i] = backup.Key
			totalSize += backup.Size
		}

		if err := s.s3Client.DeleteMultiple(ctx, keys); err != nil {
			logrus.Errorf("Failed to delete backups for service %s: %v", serviceName, err)
			result.Error = err
			continue
		}

		logrus.Infof("Deleted %d backups for service %s (%s freed)", len(toDelete), serviceName, formatBytes(totalSize))
		allDeletedBackups = append(allDeletedBackups, toDelete...)
	}

	result.DeletedBackups = allDeletedBackups
	result.TotalSize = totalSize

	// Send notification
	if len(allDeletedBackups) > 0 {
		if result.Error != nil {
			s.sendNotification(notifications.Warning, len(allDeletedBackups), totalSize, result.Error)
		} else {
			s.sendNotification(notifications.Success, len(allDeletedBackups), totalSize, nil)
		}
	}

	return result, result.Error
}

func (s *Service) selectBackupsForDeletion(backups []*storage.BackupInfo, olderThanDays, keepLatest int) []*storage.BackupInfo {
	if len(backups) == 0 {
		return nil
	}

	cutoffDate := time.Now().AddDate(0, 0, -olderThanDays)

	// Group backups by service and path
	pathGroups := make(map[string][]*storage.BackupInfo)
	for _, backup := range backups {
		key := fmt.Sprintf("%s/%s", backup.Service, backup.Path)
		pathGroups[key] = append(pathGroups[key], backup)
	}

	var toDelete []*storage.BackupInfo

	for pathKey, pathBackups := range pathGroups {
		// Sort by date (newest first)
		sort.Slice(pathBackups, func(i, j int) bool {
			return pathBackups[i].Date.After(pathBackups[j].Date)
		})

		// Keep the latest N backups regardless of age
		var candidates []*storage.BackupInfo
		if keepLatest > 0 && len(pathBackups) > keepLatest {
			candidates = pathBackups[keepLatest:]
		} else {
			candidates = pathBackups
		}

		// From the candidates, select those older than cutoff date
		for _, backup := range candidates {
			if backup.Date.Before(cutoffDate) {
				toDelete = append(toDelete, backup)
				logrus.Debugf("Marking for deletion: %s (age: %v)", backup.Key, time.Since(backup.Date))
			}
		}

		logrus.Debugf("Path %s: %d total, %d candidates, %d to delete", pathKey, len(pathBackups), len(candidates), len(toDelete))
	}

	// Sort by date (oldest first for deletion)
	sort.Slice(toDelete, func(i, j int) bool {
		return toDelete[i].Date.Before(toDelete[j].Date)
	})

	return toDelete
}

func (s *Service) sendNotification(notifType notifications.NotificationType, deletedCount int, totalSize int64, err error) {
	if s.notifier == nil {
		return
	}

	s.notifier.SendCleanupNotification(notifType, deletedCount, totalSize, err)
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}