# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Development Commands

```bash
# Build the project
go build -o stash.exe

# Build and run
go run main.go [command]

# Format code
go fmt ./...

# Vet code for potential issues
go vet ./...

# Clean build artifacts
go clean

# Update dependencies
go mod tidy

# Run all commands without tests (no test files exist)
go test ./...  # Will show "[no test files]" for all packages
```

## Environment Setup for Testing

The application requires S3-compatible storage credentials. For development/testing:

```bash
# Required environment variables
export AWS_ACCESS_KEY_ID=your-access-key
export AWS_SECRET_ACCESS_KEY=your-secret-key
export AWS_REGION=your-region

# For custom S3-compatible endpoints (e.g., DigitalOcean, Cloudflare R2)
export AWS_ENDPOINT_URL_S3=https://your-endpoint.com
```

## Architecture Overview

**Stash** is a CLI backup tool built with Cobra that backs up files to S3-compatible storage with optional Discord notifications.

### Core Components

**Configuration (`internal/config/`)**
- Uses Viper for YAML config management
- Global config accessible via `config.Get()`
- Validates S3 settings, services, and paths on load
- Configuration loaded in root command's `PersistentPreRunE`

**CLI Structure (`cmd/`)**
- Root command in `cmd/root.go` with global flags (`--config`, `--verbose`, `--no-notify`)
- Subcommands: `backup`, `restore`, `list`, `cleanup`, `config`
- Uses logrus for logging with configurable verbosity
- `SilenceUsage: true` set to avoid usage messages on errors

**Storage Layer (`internal/storage/`)**
- S3Client wraps AWS SDK v2 for S3-compatible storage
- Handles upload, download, listing, and deletion operations
- Uses environment variables for credentials and custom endpoints

**Backup/Restore Services (`internal/backup/`, `internal/restore/`)**
- Service pattern: `NewService(cfg, noNotify)` creates service instances
- BackupResult/RestoreResult structs contain operation outcomes
- Integrates with archive creation, S3 operations, and notifications

**Archive Operations (`internal/archive/`)**
- Creates tar.gz archives from file paths
- Supports selective folder inclusion via config
- Handles both compression and extraction

**Notifications (`internal/notifications/`)**
- Discord webhook integration for backup/cleanup status
- Configurable notification types (success/error/warning)
- Clean, human-readable message formatting (no emojis)

### Key Data Flow

1. **Config Loading**: Root command loads and validates YAML config
2. **Service Creation**: Commands create service instances with S3Client and notifier
3. **Operations**: Services coordinate between archive, storage, and notification layers
4. **Results**: Structured result objects contain outcomes for reporting

### Configuration Schema

Services are defined with paths and optional include_folders:
```yaml
services:
  service-name:
    paths:
      data: /absolute/path/to/data
      logs: /absolute/path/to/logs
    include_folders:
      data: ["subfolder1", "subfolder2"]
```

S3 configuration supports custom endpoints for non-AWS providers:
```yaml
s3:
  bucket: bucket-name
  prefix: backup-prefix
```