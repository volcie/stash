package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/sirupsen/logrus"
)

type S3Client struct {
	client *s3.Client
	bucket string
	prefix string
}

type BackupInfo struct {
	Service string
	Path    string
	Date    time.Time
	Key     string
	Size    int64
	ETag    string
}

func NewS3Client(bucket, prefix string) (*S3Client, error) {
	// Validate environment variables
	if err := validateS3Environment(); err != nil {
		return nil, err
	}

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	// Test connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := testS3Connectivity(ctx, client, bucket); err != nil {
		return nil, fmt.Errorf("failed to connect to S3: %w", err)
	}

	logrus.Debugf("Connected to S3-compatible storage")

	return &S3Client{
		client: client,
		bucket: bucket,
		prefix: prefix,
	}, nil
}

func (s *S3Client) Upload(ctx context.Context, reader io.Reader, service, pathName string) (*BackupInfo, error) {
	timestamp := time.Now().Format("20060102-150405")
	key := s.buildKey(service, pathName, timestamp)

	logrus.Infof("Uploading backup to s3://%s/%s", s.bucket, key)

	result, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   reader,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload to S3: %w", err)
	}

	// Get object info for size
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		logrus.Warnf("Failed to get object size: %v", err)
	}

	backupTime, _ := time.Parse("20060102-150405", timestamp)

	var size int64
	if head.ContentLength != nil {
		size = *head.ContentLength
	}

	return &BackupInfo{
		Service: service,
		Path:    pathName,
		Date:    backupTime,
		Key:     key,
		Size:    size,
		ETag:    strings.Trim(*result.ETag, "\""),
	}, nil
}

func (s *S3Client) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	logrus.Infof("Downloading backup from s3://%s/%s", s.bucket, key)

	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download from S3: %w", err)
	}

	return result.Body, nil
}

func (s *S3Client) List(ctx context.Context, service string) ([]*BackupInfo, error) {
	var prefix string
	if service != "" {
		prefix = s.buildServicePrefix(service)
	} else {
		prefix = s.prefix
	}

	logrus.Debugf("Listing S3 objects with prefix: %s", prefix)

	var backups []*BackupInfo
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		result, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list S3 objects: %w", err)
		}

		for _, obj := range result.Contents {
			backup := s.parseKey(*obj.Key)
			if backup != nil {
				if obj.Size != nil {
					backup.Size = *obj.Size
				}
				backup.ETag = strings.Trim(*obj.ETag, "\"")
				backups = append(backups, backup)
			}
		}
	}

	return backups, nil
}

func (s *S3Client) Delete(ctx context.Context, key string) error {
	logrus.Infof("Deleting backup s3://%s/%s", s.bucket, key)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete S3 object: %w", err)
	}

	return nil
}

func (s *S3Client) DeleteMultiple(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	var objects []types.ObjectIdentifier
	for _, key := range keys {
		objects = append(objects, types.ObjectIdentifier{
			Key: aws.String(key),
		})
	}

	logrus.Infof("Deleting %d backups from S3", len(keys))

	_, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &types.Delete{
			Objects: objects,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete S3 objects: %w", err)
	}

	return nil
}

func (s *S3Client) buildKey(service, pathName, timestamp string) string {
	parts := []string{s.prefix, service, pathName, fmt.Sprintf("%s.tar.gz", timestamp)}
	return strings.Join(parts, "/")
}

func (s *S3Client) buildServicePrefix(service string) string {
	return filepath.Join(s.prefix, service) + "/"
}

func (s *S3Client) parseKey(key string) *BackupInfo {
	// Expected format: prefix/service/path/timestamp.tar.gz
	if !strings.HasPrefix(key, s.prefix) {
		return nil
	}

	relativePath := strings.TrimPrefix(key, s.prefix+"/")
	parts := strings.Split(relativePath, "/")

	if len(parts) < 3 {
		return nil
	}

	service := parts[0]
	pathName := strings.Join(parts[1:len(parts)-1], "/")
	filename := parts[len(parts)-1]

	// Extract timestamp from filename
	if !strings.HasSuffix(filename, ".tar.gz") {
		return nil
	}

	timestamp := strings.TrimSuffix(filename, ".tar.gz")

	// Validate that the filename is just a timestamp (no extra parts like service-path-timestamp)
	// Expected format: YYYYMMDD-HHMMSS (exactly 15 characters)
	if len(timestamp) != 15 || timestamp[8] != '-' {
		// Not a valid timestamp format, silently skip (probably old backup format)
		return nil
	}

	date, err := time.Parse("20060102-150405", timestamp)
	if err != nil {
		// Invalid timestamp format, silently skip
		return nil
	}

	return &BackupInfo{
		Service: service,
		Path:    pathName,
		Date:    date,
		Key:     key,
	}
}

func validateS3Environment() error {
	// Check for required AWS credentials
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if accessKey == "" {
		return fmt.Errorf("AWS_ACCESS_KEY_ID environment variable is required")
	}

	if secretKey == "" {
		return fmt.Errorf("AWS_SECRET_ACCESS_KEY environment variable is required")
	}

	// Check for region (some providers require it)
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		logrus.Warn("AWS_REGION not set, using 'us-east-1' as default")
	}

	// Log S3 configuration (without sensitive data)
	endpoint := os.Getenv("AWS_ENDPOINT_URL_S3")
	if endpoint == "" {
		endpoint = os.Getenv("AWS_ENDPOINT_URL")
	}

	if endpoint != "" {
		logrus.Debugf("Using custom S3 endpoint: %s", endpoint)
	} else {
		logrus.Debug("Using AWS S3 (no custom endpoint specified)")
	}

	logrus.Debugf("S3 Configuration: AccessKey=%s..., Region=%s",
		accessKey[:min(len(accessKey), 8)],
		getOrDefault(region, "us-east-1"))

	return nil
}

func testS3Connectivity(ctx context.Context, client *s3.Client, bucket string) error {
	logrus.Debugf("Testing S3 connectivity to bucket: %s", bucket)

	// Try to head the bucket to test connectivity
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})

	if err != nil {
		return fmt.Errorf("cannot access bucket '%s': %w\n\nTroubleshooting:\n"+
			"1. Verify bucket name is correct\n"+
			"2. Check AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY\n"+
			"3. Ensure credentials have S3 permissions\n"+
			"4. For non-AWS S3, verify AWS_ENDPOINT_URL_S3 is set correctly\n"+
			"5. Check your S3 provider's documentation for region settings", bucket, err)
	}

	return nil
}

func getOrDefault(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
