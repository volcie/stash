# Stash

Simple CLI backup tool that backs up files to S3-compatible storage with Discord notifications.

## Quick Start

```bash
# Build
go build -o stash

# Create config
./stash config init

# Set credentials
export AWS_ACCESS_KEY_ID=your-key
export AWS_SECRET_ACCESS_KEY=your-secret
export AWS_REGION=your-region

# Test connection
./stash config test

# Backup all services
./stash backup all
```

## Configuration

Edit `config.yaml`:

```yaml
s3:
  bucket: my-backup-bucket
  prefix: backups

services:
  web-server:
    paths:
      data: /var/www/html
    include_folders:
      data: ["uploads", "themes"]

retention: 14

notifications:
  discord_webhook: 'https://discord.com/api/webhooks/...'
  on_success: true
  on_error: true
  on_warning: true
```

## Commands

```bash
# Backup
./stash backup all
./stash backup web-server

# List backups
./stash list
./stash list --service web-server

# Restore
./stash restore web-server
./stash restore web-server --date 20231215 --dry-run

# Cleanup old backups
./stash cleanup --older-than 30

# Config management
./stash config show
./stash config test
```

## Custom S3 Endpoints

```bash
# DigitalOcean Spaces
export AWS_ENDPOINT_URL=https://nyc3.digitaloceanspaces.com

# Cloudflare R2
export AWS_ENDPOINT_URL=https://abc123.r2.cloudflarestorage.com
```