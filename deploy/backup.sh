#!/usr/bin/env bash
# Archives the saved designs (deploy/data) and keeps the newest 14 archives.
# Run ON the VPS; deploy/README.md shows the daily cron line.
set -euo pipefail
cd "$(dirname "$0")"

dest=${ARCHSIM_BACKUP_DIR:-$HOME/backups/archsim}
mkdir -p "$dest"
file="$dest/archsim-data-$(date -u +%Y%m%dT%H%M%SZ).tgz"
# The data belongs to the container's user (mode 0700), hence sudo.
sudo -n tar -czf "$file" data
sudo -n chown "$(id -u):$(id -g)" "$file"
ls -1t "$dest"/archsim-data-*.tgz | tail -n +15 | xargs -r rm -f
echo "$file"
