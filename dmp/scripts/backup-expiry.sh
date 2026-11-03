#!/usr/bin/env bash
# Enforce the approved 90-day rolling backup expiry.
#
# Sandbox stacks keep ClickHouse backups in a directory reachable from the host:
#   DMP_BACKUP_DIR=/path/to/clickhouse-backups ./dmp/scripts/backup-expiry.sh
# Without --apply the script only reports what would be deleted.
set -euo pipefail

BACKUP_DIR=${DMP_BACKUP_DIR:-}
RETENTION_DAYS=${DMP_BACKUP_RETENTION_DAYS:-90}
APPLY=false
[ "${1:-}" = "--apply" ] && APPLY=true

if [ -z "$BACKUP_DIR" ]; then
  echo "DMP_BACKUP_DIR is not set; backup expiry has nothing to manage" >&2
  exit 2
fi
if [ ! -d "$BACKUP_DIR" ]; then
  echo "backup directory not found: $BACKUP_DIR" >&2
  exit 2
fi
if [ "$RETENTION_DAYS" -lt 1 ] || [ "$RETENTION_DAYS" -gt 90 ]; then
  echo "DMP_BACKUP_RETENTION_DAYS must be between 1 and 90 (approved ceiling)" >&2
  exit 2
fi

cutoff=$(date -u -v-"${RETENTION_DAYS}"d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "${RETENTION_DAYS} days ago" +%Y-%m-%dT%H:%M:%SZ)
echo "retention: ${RETENTION_DAYS} days (cutoff ${cutoff})"

expired=0
kept=0
while IFS= read -r -d '' file; do
  modified=$(date -u -r "$file" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$file" +%Y-%m-%dT%H:%M:%SZ)
  if [ "$modified" \< "$cutoff" ]; then
    expired=$((expired + 1))
    if [ "$APPLY" = true ]; then
      rm -f -- "$file"
      echo "deleted: $file"
    else
      echo "would delete: $file"
    fi
  else
    kept=$((kept + 1))
  fi
done < <(find "$BACKUP_DIR" -type f \( -name '*.backup' -o -name '*.zip' -o -name '*.tar*' \) -print0)

echo "expired=${expired} kept=${kept} apply=${APPLY}"
