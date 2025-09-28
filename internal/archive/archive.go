package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

type Archiver struct {
	compression bool
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