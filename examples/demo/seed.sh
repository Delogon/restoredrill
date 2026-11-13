#!/bin/sh
# Seed the demo restic repository with sample data. Idempotent: re-running
# adds a fresh snapshot, which is exactly what a real backup would do.
set -eu

export RESTIC_REPOSITORY=/repo
export RESTIC_PASSWORD_FILE=/secrets/repo.pass

if [ ! -f /repo/config ]; then
  echo "==> initializing demo restic repository"
  restic init
fi

echo "==> creating sample data"
mkdir -p /data/documents /data/photos
i=1
while [ "$i" -le 12 ]; do
  printf 'demo document %s\ncreated by the restoredrill demo seed\n' "$i" \
    > "/data/documents/doc-$i.txt"
  i=$((i + 1))
done
printf 'pretend photo bytes\n' > /data/photos/photo-1.jpg
printf 'pretend photo bytes\n' > /data/photos/photo-2.jpg

echo "==> backing up /data"
restic backup /data
restic snapshots
echo "==> seed complete"