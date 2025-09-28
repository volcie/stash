package archive

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
)

type Archiver struct {
	compression  bool
	preserveACLs bool
}

type ArchiveStats struct {
	FilesProcessed int
	TotalSize      int64
	CompressedSize int64
}

func NewArchiver(compression, preserveACLs bool) *Archiver {
	return &Archiver{
		compression:  compression,
		preserveACLs: preserveACLs,
	}
}

func (a *Archiver) CreateArchive(writer io.Writer, sourcePath string, includeFolders []string) (*ArchiveStats, error) {
	return a.CreateArchiveWithProgress(writer, sourcePath, includeFolders, nil)
}

func (a *Archiver) CreateArchiveWithProgress(writer io.Writer, sourcePath string, includeFolders []string, progressBar *progressbar.ProgressBar) (*ArchiveStats, error) {
	stats := &ArchiveStats{}

	var finalWriter io.Writer = writer
	var gzipWriter *gzip.Writer

	if a.compression {
		gzipWriter = gzip.NewWriter(writer)
		finalWriter = gzipWriter
		defer gzipWriter.Close()
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	logrus.Infof("Creating archive from %s", sourcePath)
	if len(includeFolders) > 0 {
		logrus.Infof("Including specific folders: %v", includeFolders)
	}

	err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			logrus.Warnf("Error accessing %s: %v", path, err)
			return nil // Continue processing other files
		}

		// Skip if we have include filters and this path doesn't match
		if len(includeFolders) > 0 && !a.shouldInclude(path, sourcePath, includeFolders) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Create relative path for tar archive
		relPath, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Convert Windows paths to Unix-style for tar
		relPath = filepath.ToSlash(relPath)

		// Create tar header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header: %w", err)
		}

		header.Name = relPath

		// Add ACL information to PAX headers if ACL preservation is enabled
		if a.preserveACLs {
			aclData, err := a.getFileACL(path)
			if err != nil {
				logrus.Warnf("Failed to get ACL for %s: %v", path, err)
			} else if aclData != "" {
				if header.PAXRecords == nil {
					header.PAXRecords = make(map[string]string)
				}
				header.PAXRecords["STASH.acl"] = aclData
				header.Format = tar.FormatPAX // Ensure we use PAX format for extended attributes
				logrus.Debugf("Stored ACL for %s", relPath)
			}
		}

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		// Write file content if it's a regular file
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				logrus.Warnf("Failed to open file %s: %v", path, err)
				return nil // Continue with other files
			}
			defer file.Close()

			written, err := io.Copy(tarWriter, file)
			if err != nil {
				logrus.Warnf("Failed to write file %s to archive: %v", path, err)
				return nil // Continue with other files
			}

			stats.TotalSize += written
			stats.FilesProcessed++

			// Update progress bar if provided
			if progressBar != nil {
				progressBar.Add(1)
			}

			logrus.Debugf("Added file: %s (%d bytes)", relPath, written)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create archive: %w", err)
	}

	// Close writers to ensure all data is flushed
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close tar writer: %w", err)
	}

	if gzipWriter != nil {
		if err := gzipWriter.Close(); err != nil {
			return nil, fmt.Errorf("failed to close gzip writer: %w", err)
		}
	}

	logrus.Infof("Archive created successfully: %d files, %d bytes", stats.FilesProcessed, stats.TotalSize)

	return stats, nil
}

func (a *Archiver) ExtractArchive(reader io.Reader, destPath string) error {
	return a.ExtractArchiveWithProgress(reader, destPath, nil)
}

func (a *Archiver) ExtractArchiveWithProgress(reader io.Reader, destPath string, progressBar *progressbar.ProgressBar) error {
	var finalReader io.Reader = reader

	// Try to detect if it's gzipped
	if a.compression {
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			logrus.Warnf("Failed to create gzip reader, assuming uncompressed: %v", err)
			finalReader = reader
		} else {
			finalReader = gzipReader
			defer gzipReader.Close()
		}
	}

	tarReader := tar.NewReader(finalReader)

	logrus.Infof("Extracting archive to %s", destPath)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Convert Unix-style paths back to OS-specific paths
		targetPath := filepath.Join(destPath, filepath.FromSlash(header.Name))

		// Ensure the target directory exists
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", targetPath, err)
			}
		case tar.TypeReg:
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", targetPath, err)
			}

			if _, err := io.Copy(file, tarReader); err != nil {
				file.Close()
				return fmt.Errorf("failed to extract file %s: %w", targetPath, err)
			}

			file.Close()
			logrus.Debugf("Extracted file: %s", header.Name)
		default:
			logrus.Warnf("Unsupported file type for %s: %c", header.Name, header.Typeflag)
		}

		// Restore ACL information if present
		if a.preserveACLs && header.PAXRecords != nil {
			if aclData, exists := header.PAXRecords["STASH.acl"]; exists && aclData != "" {
				if err := a.setFileACL(targetPath, aclData); err != nil {
					logrus.Warnf("Failed to restore ACL for %s: %v", targetPath, err)
					// Continue processing - ACL restoration failure shouldn't stop extraction
				} else {
					logrus.Debugf("Restored ACL for %s", header.Name)
				}
			}
		}

		// Update progress bar if provided
		if progressBar != nil {
			progressBar.Add(1)
		}
	}

	logrus.Info("Archive extracted successfully")
	return nil
}

func (a *Archiver) shouldInclude(path, basePath string, includeFolders []string) bool {
	relPath, err := filepath.Rel(basePath, path)
	if err != nil {
		return false
	}

	// Always include the base path itself
	if relPath == "." {
		return true
	}

	// Check if any of the include folders match the start of this path
	for _, includeFolder := range includeFolders {
		// Normalize path separators
		includeFolder = filepath.FromSlash(includeFolder)

		if strings.HasPrefix(relPath, includeFolder) {
			return true
		}

		// Also check if this is a parent directory of an include folder
		if strings.HasPrefix(includeFolder, relPath) {
			return true
		}
	}

	return false
}

// getFileACL extracts ACL information from a file in a platform-specific way
func (a *Archiver) getFileACL(path string) (string, error) {
	if !a.preserveACLs {
		return "", nil
	}

	switch runtime.GOOS {
	case "linux", "darwin", "freebsd":
		return a.getUnixACL(path)
	case "windows":
		return a.getWindowsACL(path)
	default:
		logrus.Debugf("ACL preservation not supported on %s", runtime.GOOS)
		return "", nil
	}
}

// setFileACL applies ACL information to a file in a platform-specific way
func (a *Archiver) setFileACL(path, aclData string) error {
	if !a.preserveACLs || aclData == "" {
		return nil
	}

	switch runtime.GOOS {
	case "linux", "darwin", "freebsd":
		return a.setUnixACL(path, aclData)
	case "windows":
		return a.setWindowsACL(path, aclData)
	default:
		logrus.Debugf("ACL preservation not supported on %s", runtime.GOOS)
		return nil
	}
}

// getUnixACL gets ACL data using getfacl command
func (a *Archiver) getUnixACL(path string) (string, error) {
	// Check if getfacl is available
	if _, err := exec.LookPath("getfacl"); err != nil {
		logrus.Debugf("getfacl command not found, skipping ACL extraction")
		return "", nil
	}

	cmd := exec.Command("getfacl", "-p", path)
	output, err := cmd.Output()
	if err != nil {
		// getfacl might fail if file doesn't have extended ACLs or other issues
		logrus.Debugf("Failed to get ACL for %s: %v", path, err)
		return "", nil
	}

	// Only store if we actually got meaningful ACL data
	if len(output) > 0 {
		// Base64 encode the ACL data for safe storage in tar headers
		return base64.StdEncoding.EncodeToString(output), nil
	}

	return "", nil
}

// setUnixACL sets ACL data using setfacl command
func (a *Archiver) setUnixACL(path, aclData string) error {
	// Check if setfacl is available
	if _, err := exec.LookPath("setfacl"); err != nil {
		logrus.Debugf("setfacl command not found, skipping ACL restoration")
		return nil
	}

	// Decode the base64 ACL data
	decoded, err := base64.StdEncoding.DecodeString(aclData)
	if err != nil {
		return fmt.Errorf("failed to decode ACL data: %w", err)
	}

	// Create a temporary file with ACL rules
	tmpFile, err := os.CreateTemp("", "acl_rules")
	if err != nil {
		return fmt.Errorf("failed to create temp ACL file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(decoded); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write ACL rules: %w", err)
	}
	tmpFile.Close()

	// Apply ACL using setfacl
	cmd := exec.Command("setfacl", "--restore", tmpFile.Name())
	if err := cmd.Run(); err != nil {
		logrus.Warnf("Failed to set ACL for %s: %v", path, err)
		return nil // Don't fail the entire operation for ACL issues
	}

	return nil
}

// getWindowsACL gets Windows ACL data using icacls command
func (a *Archiver) getWindowsACL(path string) (string, error) {
	// Check if icacls is available
	if _, err := exec.LookPath("icacls"); err != nil {
		logrus.Debugf("icacls command not found, skipping ACL extraction")
		return "", nil
	}

	// Use icacls to get ACL data
	// Note: A more robust implementation would use the Windows API directly
	cmd := exec.Command("icacls", path, "/save", "-")
	output, err := cmd.Output()
	if err != nil {
		logrus.Debugf("Failed to get Windows ACL for %s: %v", path, err)
		return "", nil
	}

	// Only store if we got meaningful data
	if len(output) > 0 {
		return base64.StdEncoding.EncodeToString(output), nil
	}

	return "", nil
}

// setWindowsACL sets Windows ACL data using icacls command
func (a *Archiver) setWindowsACL(path, aclData string) error {
	if aclData == "" {
		return nil
	}

	// Check if icacls is available
	if _, err := exec.LookPath("icacls"); err != nil {
		logrus.Debugf("icacls command not found, skipping ACL restoration")
		return nil
	}

	// Decode the ACL data
	decoded, err := base64.StdEncoding.DecodeString(aclData)
	if err != nil {
		return fmt.Errorf("failed to decode Windows ACL data: %w", err)
	}

	// Create temporary file for ACL data
	tmpFile, err := os.CreateTemp("", "windows_acl")
	if err != nil {
		return fmt.Errorf("failed to create temp ACL file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(decoded); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write ACL data: %w", err)
	}
	tmpFile.Close()

	// Apply ACL using icacls
	cmd := exec.Command("icacls", path, "/restore", tmpFile.Name())
	if err := cmd.Run(); err != nil {
		logrus.Warnf("Failed to set Windows ACL for %s: %v", path, err)
		return nil // Don't fail the entire operation for ACL issues
	}

	return nil
}

// CountFiles counts the number of files that will be processed for progress tracking
func (a *Archiver) CountFiles(sourcePath string, includeFolders []string) (int, error) {
	count := 0
	err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue processing other files
		}

		// Skip if we have include filters and this path doesn't match
		if len(includeFolders) > 0 && !a.shouldInclude(path, sourcePath, includeFolders) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Count regular files
		if info.Mode().IsRegular() {
			count++
		}

		return nil
	})

	return count, err
}
