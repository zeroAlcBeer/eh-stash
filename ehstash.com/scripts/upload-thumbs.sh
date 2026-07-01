#!/usr/bin/env bash
# Bulk-upload thumbs from the local tar to R2 via rclone.
#
# `wrangler r2 object put` spawns one Node process per file — 157k files
# would take ~40 hours. rclone with parallelism does it in 10-30 minutes.
#
# Prereqs:
#   1. brew install rclone
#   2. Create an R2 API token in CF dash → R2 → Manage API tokens
#      (Wrangler can't create S3-credential R2 tokens — this is the one
#       step that must happen in the dashboard.)
#   3. rclone config — add a remote, type "s3", provider "Cloudflare",
#      paste the Access Key ID + Secret Access Key, endpoint:
#        https://<ACCOUNT_ID>.r2.cloudflarestorage.com
#      (Your account id: e77f59c260bfe0ae47e1e998e989eea0)
#   4. data/thumbs.tar present
#
# Usage:
#   ./scripts/upload-thumbs.sh [rclone-remote-name]
#
# Default remote name is "r2". Override if you named yours differently.

set -euo pipefail

cd "$(dirname "$0")/.."

REMOTE="${1:-r2}"
BUCKET="eh-stash-thumbs"
TAR="data/thumbs.tar"
EXTRACT_DIR="data/thumbs"

if ! command -v rclone >/dev/null; then
  echo "error: rclone not installed. brew install rclone" >&2
  exit 1
fi

if [[ ! -f "$TAR" ]]; then
  echo "error: $TAR not found" >&2
  exit 1
fi

# Extract once. The user explicitly asked us not to extract eagerly during
# testing, so this runs only on a real upload pass.
if [[ ! -d "$EXTRACT_DIR" ]] || [[ -z "$(ls -A "$EXTRACT_DIR" 2>/dev/null)" ]]; then
  echo "==> extracting $TAR to $EXTRACT_DIR (this is ~3.7G)"
  mkdir -p "$EXTRACT_DIR"
  tar xf "$TAR" -C "$EXTRACT_DIR"
fi

COUNT=$(find "$EXTRACT_DIR" -maxdepth 1 -type f | wc -l | tr -d ' ')
echo "==> $COUNT files staged in $EXTRACT_DIR"

echo "==> uploading to $REMOTE:$BUCKET (parallel)"
rclone copy "$EXTRACT_DIR" "$REMOTE:$BUCKET" \
  --transfers=32 \
  --checkers=16 \
  --s3-upload-concurrency=4 \
  --progress \
  --no-traverse \
  --header-upload="Content-Type: image/jpeg" \
  --header-upload="Cache-Control: public, max-age=604800, immutable"

echo "==> done. Remove local extracted copy with:"
echo "    rm -rf $EXTRACT_DIR"
