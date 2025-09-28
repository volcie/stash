package backup

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/volcie/stash/internal/archive"
	"github.com/volcie/stash/internal/config"
	"github.com/volcie/stash/internal/notifications"
	"github.com/volcie/stash/internal/storage"
)

type Service struct {
	cfg      *config.Config
	s3Client *storage.S3Client
	notifier *notifications.DiscordNotifier
}

type BackupResult struct {
	Service     string
	Path        string
	BackupInfo  *storage.BackupInfo
	ArchiveSize int64
	Duration    time.Duration
	Error       error
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

func (s *Service) BackupService(ctx context.Context, serviceName string, specificPaths []string) ([]*BackupResult, error) {
	serviceConfig, exists := s.cfg.Services[serviceName]
	if !exists {
		return nil, fmt.Errorf("service %s not found in configuration", serviceName)
	}

	var results []*BackupResult

	// Filter paths if specific paths are requested
	pathsToBackup := serviceConfig.Paths
	if len(specificPaths) > 0 {
		pathsToBackup = make(map[string]string)
		for _, pathName := range specificPaths {
			if path, exists := serviceConfig.Paths[pathName]; exists {
				pathsToBackup[pathName] = path
			} else {
				logrus.Warnf("Path %s not found in service %s configuration", pathName, serviceName)
			}
		}
	}

	if len(pathsToBackup) == 0 {
		return nil, fmt.Errorf("no valid paths to backup for service %s", serviceName)
	}

	logrus.Infof("Starting backup for service: %s (%d paths)", serviceName, len(pathsToBackup))

	for pathName, pathLocation := range pathsToBackup {
		result := s.backupPath(ctx, serviceName, pathName, pathLocation, serviceConfig.IncludeFolders[pathName])
		results = append(results, result)

		// Send individual notifications for each path
		if result.Error != nil {
			s.sendNotification(notifications.Error, serviceName, "backup", result, result.Error)
		} else {
			s.sendNotification(notifications.Success, serviceName, "backup", result, nil)
		}
	}

	return results, nil
}

func (s *Service) BackupAll(ctx context.Context, specificPaths []string) (map[string][]*BackupResult, error) {
	allResults := make(map[string][]*BackupResult)

	logrus.Infof("Starting backup for all services (%d services)", len(s.cfg.Services))

	for serviceName := range s.cfg.Services {
		results, err := s.BackupService(ctx, serviceName, specificPaths)
		if err != nil {
			logrus.Errorf("Failed to backup service %s: %v", serviceName, err)
			// Continue with other services
			allResults[serviceName] = []*BackupResult{{
				Service: serviceName,
				Error:   err,
			}}
		} else {
			allResults[serviceName] = results
		}
	}

	return allResults, nil
}

func (s *Service) backupPath(ctx context.Context, serviceName, pathName, pathLocation string, includeFolders []string) *BackupResult {
	startTime := time.Now()

	result := &BackupResult{
		Service: serviceName,
		Path:    pathName,
	}

	logrus.Infof("Backing up %s:%s from %s", serviceName, pathName, pathLocation)

	// Check if source path exists
	if _, err := os.Stat(pathLocation); err != nil {
		result.Error = fmt.Errorf("source path does not exist: %s", pathLocation)
		return result
	}

	// Create temporary file for archive
	tempDir := s.cfg.Backup.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}

	if err := os.MkdirAll(tempDir, 0755); err != nil {
		result.Error = fmt.Errorf("failed to create temp directory: %w", err)
		return result
	}

	tempFile, err := os.CreateTemp(tempDir, fmt.Sprintf("stash-%s-%s-*.tar.gz", serviceName, pathName))
	if err != nil {
		result.Error = fmt.Errorf("failed to create temp file: %w", err)
		return result
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Create archive
	archiver := archive.NewArchiver(s.cfg.Backup.Compression, s.cfg.Backup.PreserveACLs)
	stats, err := archiver.CreateArchive(tempFile, pathLocation, includeFolders)
	if err != nil {
		result.Error = fmt.Errorf("failed to create archive: %w", err)
		return result
	}

	// Get file size
	fileInfo, err := tempFile.Stat()
	if err != nil {
		result.Error = fmt.Errorf("failed to get archive size: %w", err)
		return result
	}

	result.ArchiveSize = fileInfo.Size()

	// Validate minimum size
	if s.cfg.Backup.MinSize > 0 && result.ArchiveSize < s.cfg.Backup.MinSize {
		result.Error = fmt.Errorf("archive size (%d bytes) is below minimum threshold (%d bytes)", result.ArchiveSize, s.cfg.Backup.MinSize)
		return result
	}

	// Seek back to beginning for upload
	if _, err := tempFile.Seek(0, 0); err != nil {
		result.Error = fmt.Errorf("failed to seek temp file: %w", err)
		return result
	}

	// Upload to S3
	backupInfo, err := s.s3Client.Upload(ctx, tempFile, serviceName, pathName)
	if err != nil {
		result.Error = fmt.Errorf("failed to upload to S3: %w", err)
		return result
	}

	result.BackupInfo = backupInfo
	result.Duration = time.Since(startTime)

	logrus.Infof("Backup completed for %s:%s - %d files, %s uploaded in %v",
		serviceName, pathName, stats.FilesProcessed, formatBytes(result.ArchiveSize), result.Duration)

	return result
}

func (s *Service) sendNotification(notifType notifications.NotificationType, serviceName, operation string, result *BackupResult, err error) {
	if s.notifier == nil {
		return
	}

	details := make(map[string]string)
	details["Service"] = serviceName
	details["Path"] = result.Path

	if result.Duration > 0 {
		details["Duration"] = result.Duration.String()
	}

	if result.ArchiveSize > 0 {
		details["Archive Size"] = formatBytes(result.ArchiveSize)
	}

	if result.BackupInfo != nil {
		details["S3 Key"] = result.BackupInfo.Key
		details["Backup Time"] = result.BackupInfo.Date.Format("2006-01-02 15:04:05")
	}

	s.notifier.SendBackupNotification(notifType, serviceName, operation, details, err)
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
