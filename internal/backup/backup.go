package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
	"github.com/volcie/stash/internal/archive"
	"github.com/volcie/stash/internal/cleanup"
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

	// Auto-cleanup old backups if enabled and backup was successful
	if s.cfg.AutoCleanup {
		s.performAutoCleanup(ctx, serviceName, results)
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

	// Create archive with progress bar
	archiver := archive.NewArchiver(s.cfg.Backup.Compression, s.cfg.Backup.PreserveACLs)

	// Count files for progress tracking
	fileCount, err := archiver.CountFiles(pathLocation, includeFolders)
	if err != nil {
		logrus.Warnf("Failed to count files for progress tracking: %v", err)
		fileCount = 100 // Fallback estimate
	}

	// Create progress bar
	fmt.Println() // Add line break before progress bar
	progressBar := progressbar.NewOptions(fileCount,
		progressbar.OptionSetDescription(fmt.Sprintf("Compressing %s/%s", serviceName, pathName)),
		progressbar.OptionSetWidth(40),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)

	stats, err := archiver.CreateArchiveWithProgress(tempFile, pathLocation, includeFolders, progressBar)
	if err != nil {
		result.Error = fmt.Errorf("failed to create archive: %w", err)
		return result
	}

	// Finish progress bar and add newline
	progressBar.Finish()
	fmt.Print("\n") // Add newline after progress bar

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

	// Upload to S3 with progress bar
	fmt.Println() // Add line break before progress bar
	uploadProgressBar := progressbar.NewOptions(int(result.ArchiveSize),
		progressbar.OptionSetDescription(fmt.Sprintf("Uploading %s/%s to S3", serviceName, pathName)),
		progressbar.OptionSetWidth(40),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)

	// Try uploading with progress tracking first
	progressReader := &progressReader{
		reader:      tempFile,
		progressBar: uploadProgressBar,
	}

	backupInfo, err := s.s3Client.Upload(ctx, progressReader, serviceName, pathName)
	if err != nil {
		// If upload with progress tracking fails, try without it
		logrus.Warnf("Upload with progress tracking failed, retrying without progress: %v", err)
		uploadProgressBar.Describe("Uploading (fallback mode)")

		// Reset file position
		if _, seekErr := tempFile.Seek(0, 0); seekErr != nil {
			result.Error = fmt.Errorf("failed to seek temp file for retry: %w", seekErr)
			return result
		}

		// Try upload without progress wrapper
		backupInfo, err = s.s3Client.Upload(ctx, tempFile, serviceName, pathName)
		if err != nil {
			result.Error = fmt.Errorf("failed to upload to S3: %w", err)
			return result
		}

		// Complete progress bar manually since we couldn't track it
		uploadProgressBar.Set(int(result.ArchiveSize))
	}

	// Finish upload progress bar
	uploadProgressBar.Finish()
	fmt.Println() // Add newline after progress bar

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

// performAutoCleanup runs cleanup for the specified service only if the backup was successful
func (s *Service) performAutoCleanup(ctx context.Context, serviceName string, results []*BackupResult) {
	// Only run cleanup if at least one backup was successful
	hasSuccessfulBackup := false
	for _, result := range results {
		if result.Error == nil {
			hasSuccessfulBackup = true
			break
		}
	}

	if !hasSuccessfulBackup {
		logrus.Debugf("Skipping auto-cleanup for service %s - no successful backups", serviceName)
		return
	}

	logrus.Infof("Auto-cleanup enabled, cleaning up old backups for service: %s", serviceName)

	// Create cleanup service and run cleanup for this specific service
	cleanupService, err := cleanup.NewService(s.cfg, s.notifier == nil) // Use same no-notify setting as backup
	if err != nil {
		logrus.Warnf("Failed to initialize cleanup service for auto-cleanup: %v", err)
		return
	}

	// Run cleanup for this specific service only
	cleanupOpts := &cleanup.CleanupOptions{
		ServiceName: serviceName,
		OlderThan:   s.cfg.Retention, // Use configured retention period
		DryRun:      false,           // Actually perform cleanup
		KeepLatest:  1,               // Always keep at least the latest backup
	}

	result, err := cleanupService.CleanupBackups(ctx, cleanupOpts)
	if err != nil {
		logrus.Warnf("Auto-cleanup failed for service %s: %v", serviceName, err)
		return
	}

	if result.Error != nil {
		logrus.Warnf("Auto-cleanup encountered error for service %s: %v", serviceName, result.Error)
		return
	}

	// Log cleanup results
	deletedCount := len(result.DeletedBackups)
	if deletedCount > 0 {
		logrus.Infof("Auto-cleanup completed: removed %d old backups for service %s", deletedCount, serviceName)
	} else {
		logrus.Debugf("Auto-cleanup completed: no old backups to remove for service %s", serviceName)
	}
}

// progressReader wraps an io.Reader to update a progress bar as data is read
type progressReader struct {
	reader      io.Reader
	progressBar *progressbar.ProgressBar
}

func (pr *progressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	if n > 0 && pr.progressBar != nil {
		pr.progressBar.Add(n)
	}
	return n, err
}

// Implement io.Seeker if the underlying reader supports it
func (pr *progressReader) Seek(offset int64, whence int) (int64, error) {
	if seeker, ok := pr.reader.(io.Seeker); ok {
		return seeker.Seek(offset, whence)
	}
	return 0, fmt.Errorf("underlying reader does not support seeking")
}

// Implement io.Closer if the underlying reader supports it
func (pr *progressReader) Close() error {
	if closer, ok := pr.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
