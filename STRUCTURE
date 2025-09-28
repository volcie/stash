stash
├── backup
│   ├── all                     # Backup all services
│   ├── [service_name]          # Backup XenForo (DB + files)
│   └── --paths list,of,paths   # Flag: backup only these paths
│
├── restore
│   ├── [service_name]
│   ├── --from-s3               # Flag: restore from S3 (default)
│   ├── --from-local PATH       # Flag: restore from local file
│   ├── --date YYYYMMDD         # Flag: specific backup date
│   ├── --latest                # Flag: use latest backup (default)
│   ├── --dry-run               # Flag: show what would be restored
│   └── --force                 # Flag: skip confirmation prompts
│
├── list
│   ├── --service NAME          # Filter by service
│   ├── --local                 # List local backups
│   └── --s3                    # List S3 backups (default)
│
├── cleanup
│   ├── --service NAME/all      # Cleanup specific service only
│   ├── --older-than DAYS       # Delete backups older than X days
│   ├── --dry-run               # Show what would be deleted
│   └── --keep-latest N         # Always keep N latest backups
│
├── config
│   ├── init                    # Interactive setup wizard
│   ├── show                    # Display current config
│   └── edit                    # Open config in editor
│
└── Global Flags:
    ├── --config PATH           # Use alternate config file
    ├── --verbose               # Verbose output
    └── --no-notify             # Skip Discord notifications