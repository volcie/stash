package restore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/schollz/progressbar/v3"
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

type RestoreOptions struct {
	ServiceName string
	FromS3      bool
	FromLocal   string
	Date        string
	Latest      bool
	DryRun      bool
	Force       bool
	DestPath    string
}

type RestoreResult struct {
	Service     string
	Path        string
	BackupInfo  *storage.BackupInfo
	RestorePath string
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

func (s *Service) RestoreService(ctx context.Context, opts *RestoreOptions) ([]*RestoreResult, error) {
	serviceConfig, exists := s.cfg.Services[opts.ServiceName]
	if !exists {
		return nil, fmt.Errorf("service %s not found in configuration", opts.ServiceName)
	}

	var results []*RestoreResult

	if opts.FromLocal != "" {
		return s.restoreFromLocal(opts)
	}

	// Get available backups from S3
	backups, err := s.s3Client.List(ctx, opts.ServiceName)
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}

	if len(backups) == 0 {
		return nil, fmt.Errorf("no backups found for service %s", opts.ServiceName)
	}

	// Filter and select backups
	selectedBackups := s.selectBackups(backups, opts)
	if len(selectedBackups) == 0 {
		return nil, fmt.Errorf("no backups match the specified criteria")
	}

	logrus.Infof("Found %d backups to restore for service: %s", len(selectedBackups), opts.ServiceName)

	for _, backup := range selectedBackups {
		pathConfig, exists := serviceConfig.Paths[backup.Path]
		if !exists {
			logrus.Warnf("Path %s not found in current service configuration, skipping", backup.Path)
			continue
		}

		destPath := pathConfig
		if opts.DestPath != "" {
			destPath = filepath.Join(opts.DestPath, backup.Path)
		}

		result := s.restoreBackup(ctx, backup, destPath, opts)
		results = append(results, result)

		// Send notifications (skip during dry run)
		if !opts.DryRun {
			if result.Error != nil {
				s.sendNotification(notifications.Error, opts.ServiceName, "restore", result, result.Error)
			} else {
				s.sendNotification(notifications.Success, opts.ServiceName, "restore", result, nil)
			}
		}
	}

	return results, nil
}

func (s *Service) selectBackups(backups []*storage.BackupInfo, opts *RestoreOptions) []*storage.BackupInfo {
	var filtered []*storage.BackupInfo

	// Filter by date if specified
	if opts.Date != "" {
		targetDate, isExactTime, err := s.parseDate(opts.Date)
		if err != nil {
			logrus.Warnf("Invalid date format %s, ignoring date filter", opts.Date)
		} else {
			for _, backup := range backups {
				var matches bool
				if isExactTime {
					// Exact timestamp match (YYYYMMDD-HHMMSS)
					matches = backup.Date.Format("20060102-150405") == targetDate.Format("20060102-150405")
				} else {
					// Date only match (YYYYMMDD) - match any backup from that day
					matches = backup.Date.Format("20060102") == targetDate.Format("20060102")
				}

				if matches {
					filtered = append(filtered, backup)
				}
			}
			backups = filtered
		}
	}

	if len(backups) == 0 {
		return nil
	}

	// Group by path and get latest for each
	pathGroups := make(map[string][]*storage.BackupInfo)
	for _, backup := range backups {
		pathGroups[backup.Path] = append(pathGroups[backup.Path], backup)
	}

	var selected []*storage.BackupInfo
	for _, pathBackups := range pathGroups {
		// Sort by date (newest first)
		sort.Slice(pathBackups, func(i, j int) bool {
			return pathBackups[i].Date.After(pathBackups[j].Date)
		})

		if opts.Latest || opts.Date == "" {
			// Take the latest backup for this path
			selected = append(selected, pathBackups[0])
		} else {
			// Take all backups for this path (if date was specified and matched)
			selected = append(selected, pathBackups...)
		}
	}

	return selected
}

func (s *Service) restoreBackup(ctx context.Context, backup *storage.BackupInfo, destPath string, opts *RestoreOptions) *RestoreResult {
	startTime := time.Now()

	result := &RestoreResult{
		Service:     backup.Service,
		Path:        backup.Path,
		BackupInfo:  backup,
		RestorePath: destPath,
	}

	logrus.Infof("Restoring %s:%s to %s", backup.Service, backup.Path, destPath)

	if opts.DryRun {
		logrus.Infof("[DRY RUN] Would restore backup %s to %s", backup.Key, destPath)
		result.Duration = time.Since(startTime)
		return result
	}

	// Check if destination exists and ask for confirmation
	if !opts.Force {
		if _, err := os.Stat(destPath); err == nil {
			result.Error = fmt.Errorf("destination path %s already exists, use --force to overwrite", destPath)
			return result
		}
	}

	// Create destination directory
	if err := os.MkdirAll(destPath, 0755); err != nil {
		result.Error = fmt.Errorf("failed to create destination directory: %w", err)
		return result
	}

	// Download from S3 with progress bar
	fmt.Println() // Add line break before progress bar
	downloadProgressBar := progressbar.NewOptions(int(backup.Size),
		progressbar.OptionSetDescription(fmt.Sprintf("Downloading %s/%s", backup.Service, backup.Path)),
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

	reader, err := s.s3Client.Download(ctx, backup.Key)
	if err != nil {
		result.Error = fmt.Errorf("failed to download backup: %w", err)
		return result
	}
	defer reader.Close()

	// Wrap reader with progress tracking
	progressReader := &progressReadCloser{
		ReadCloser:  reader,
		progressBar: downloadProgressBar,
	}

	// Use progress reader for extraction
	defer func() {
		downloadProgressBar.Finish()
		fmt.Println() // Add newline after progress bar
	}()

	// Extract archive with progress bar
	// Note: For extraction, we use an indeterminate progress bar since tar doesn't provide total count upfront
	fmt.Println() // Add line break before progress bar
	extractProgressBar := progressbar.NewOptions(-1,
		progressbar.OptionSetDescription(fmt.Sprintf("Extracting %s/%s", backup.Service, backup.Path)),
		progressbar.OptionSetWidth(40),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)

	archiver := archive.NewArchiver(s.cfg.Backup.Compression, s.cfg.Backup.PreserveACLs)
	if err := archiver.ExtractArchiveWithProgress(progressReader, destPath, extractProgressBar); err != nil {
		result.Error = fmt.Errorf("failed to extract archive: %w", err)
		return result
	}

	// Finish extraction progress bar
	extractProgressBar.Finish()
	fmt.Println() // Add newline after progress bar

	result.Duration = time.Since(startTime)

	logrus.Infof("Restore completed for %s:%s in %v", backup.Service, backup.Path, result.Duration)
	return result
}

func (s *Service) restoreFromLocal(opts *RestoreOptions) ([]*RestoreResult, error) {
	file, err := os.Open(opts.FromLocal)
	if err != nil {
		return nil, fmt.Errorf("failed to open local file: %w", err)
	}
	defer file.Close()

	destPath := opts.DestPath
	if destPath == "" {
		return nil, fmt.Errorf("destination path is required when restoring from local file")
	}

	result := &RestoreResult{
		Service:     opts.ServiceName,
		Path:        "local",
		RestorePath: destPath,
	}

	startTime := time.Now()

	if opts.DryRun {
		logrus.Infof("[DRY RUN] Would restore local file %s to %s", opts.FromLocal, destPath)
		result.Duration = time.Since(startTime)
		return []*RestoreResult{result}, nil
	}

	// Check if destination exists and ask for confirmation
	if !opts.Force {
		if _, err := os.Stat(destPath); err == nil {
			return nil, fmt.Errorf("destination path %s already exists, use --force to overwrite", destPath)
		}
	}

	// Create destination directory
	if err := os.MkdirAll(destPath, 0755); err != nil {
		result.Error = fmt.Errorf("failed to create destination directory: %w", err)
		return []*RestoreResult{result}, nil
	}

	// Extract archive with progress bar
	fmt.Println() // Add line break before progress bar
	extractProgressBar := progressbar.NewOptions(-1,
		progressbar.OptionSetDescription(fmt.Sprintf("Extracting %s", filepath.Base(opts.FromLocal))),
		progressbar.OptionSetWidth(40),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "|",
			BarEnd:        "|",
		}),
	)

	archiver := archive.NewArchiver(s.cfg.Backup.Compression, s.cfg.Backup.PreserveACLs)
	if err := archiver.ExtractArchiveWithProgress(file, destPath, extractProgressBar); err != nil {
		result.Error = fmt.Errorf("failed to extract archive: %w", err)
		return []*RestoreResult{result}, nil
	}

	// Finish extraction progress bar
	extractProgressBar.Finish()
	fmt.Println() // Add newline after progress bar

	result.Duration = time.Since(startTime)

	logrus.Infof("Local restore completed in %v", result.Duration)
	return []*RestoreResult{result}, nil
}

func (s *Service) sendNotification(notifType notifications.NotificationType, serviceName, operation string, result *RestoreResult, err error) {
	if s.notifier == nil {
		return
	}

	details := make(map[string]string)
	details["Service"] = serviceName
	details["Path"] = result.Path
	details["Restore Path"] = result.RestorePath

	if result.Duration > 0 {
		details["Duration"] = result.Duration.String()
	}

	if result.BackupInfo != nil {
		details["Backup Date"] = result.BackupInfo.Date.Format("2006-01-02 15:04:05")
		details["S3 Key"] = result.BackupInfo.Key
	}

	s.notifier.SendBackupNotification(notifType, serviceName, operation, details, err)
}

// parseDate parses date string in either YYYYMMDD or YYYYMMDD-HHMMSS format
// Returns the parsed time, whether it's an exact timestamp (vs date-only), and any error
func (s *Service) parseDate(dateStr string) (time.Time, bool, error) {
	// Try full timestamp format first (YYYYMMDD-HHMMSS)
	if len(dateStr) == 15 && dateStr[8] == '-' {
		t, err := time.Parse("20060102-150405", dateStr)
		if err == nil {
			return t, true, nil // isExactTime = true
		}
	}

	// Try date-only format (YYYYMMDD)
	if len(dateStr) == 8 {
		t, err := time.Parse("20060102", dateStr)
		if err == nil {
			return t, false, nil // isExactTime = false
		}
	}

	return time.Time{}, false, fmt.Errorf("invalid date format: expected YYYYMMDD or YYYYMMDD-HHMMSS, got %s", dateStr)
}

// progressReadCloser wraps an io.ReadCloser to update a progress bar as data is read
type progressReadCloser struct {
	io.ReadCloser
	progressBar *progressbar.ProgressBar
}

func (prc *progressReadCloser) Read(p []byte) (n int, err error) {
	n, err = prc.ReadCloser.Read(p)
	if n > 0 && prc.progressBar != nil {
		prc.progressBar.Add(n)
	}
	return n, err
}
