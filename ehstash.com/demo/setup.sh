#!/usr/bin/env bash
# Bring up the local demo from scratch:
#   1. Start docker postgres on :5433 + caddy thumb server on :8080
#   2. Apply schema
#   3. Restore data/data.dump (full ~284MB into local PG — fast & cheap)
#   4. Subsample to SAMPLE_SIZE rows (default 2000) by fav_count, keeping
#      group siblings so the group modal stays meaningful
#   5. Extract only the corresponding thumbs from data/thumbs.tar
#
# Idempotent — running twice nukes the previous demo state and rebuilds.
#
# Prereqs: docker, psql, pg_restore, tar.
#
# Usage:
#   ./demo/setup.sh                # 2000-row sample
#   SAMPLE_SIZE=500 ./demo/setup.sh  # smaller, faster
#   SAMPLE_SIZE=0 ./demo/setup.sh    # keep everything (no sampling)

set -euo pipefail

cd "$(dirname "$0")"

DUMP="../data/data.dump"
TAR="../data/thumbs.tar"
SCHEMA="../schema/001_init.sql"
SAMPLE_SIZE="${SAMPLE_SIZE:-2000}"
PG="postgresql://postgres:postgres@localhost:5433/eh_stash"

# ── Prereqs ─────────────────────────────────────────────────────────────────
for cmd in docker psql pg_restore tar; do
  command -v "$cmd" >/dev/null || { echo "error: $cmd not installed"; exit 1; }
done
[[ -f "$DUMP" ]] || { echo "error: $DUMP not found"; exit 1; }
[[ -f "$TAR"  ]] || { echo "error: $TAR not found"; exit 1; }

# ── 1. Start postgres ───────────────────────────────────────────────────────
echo "==> starting docker services"
docker compose up -d postgres

echo "==> waiting for postgres healthy"
for i in {1..60}; do
  if docker compose exec -T postgres pg_isready -U postgres -d eh_stash >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

# ── 2. Nuke & re-apply schema (idempotent) ─────────────────────────────────
echo "==> applying schema"
psql "$PG" -v ON_ERROR_STOP=1 <<SQL
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
SQL
psql "$PG" -v ON_ERROR_STOP=1 -f "$SCHEMA"

# ── 3. Restore full data ────────────────────────────────────────────────────
echo "==> restoring data (full ~284MB, takes ~30s)"
pg_restore \
  --dbname="$PG" \
  --data-only \
  --no-owner \
  --no-privileges \
  --disable-triggers \
  --single-transaction \
  "$DUMP"

# ── 4. Sample ───────────────────────────────────────────────────────────────
if [[ "$SAMPLE_SIZE" -gt 0 ]]; then
  echo "==> sampling top $SAMPLE_SIZE by fav_count (+ group siblings)"
  psql "$PG" -v ON_ERROR_STOP=1 <<SQL
BEGIN;
CREATE TEMP TABLE keep_gids AS
  WITH top_n AS (
    SELECT gid FROM eh_galleries
    WHERE is_active = TRUE
    ORDER BY fav_count DESC NULLS LAST
    LIMIT $SAMPLE_SIZE
  ),
  sibling_groups AS (
    SELECT DISTINCT group_id FROM gallery_group_members
    WHERE gid IN (SELECT gid FROM top_n)
  )
  SELECT gid FROM top_n
  UNION
  SELECT gid FROM gallery_group_members
  WHERE group_id IN (SELECT group_id FROM sibling_groups);

DELETE FROM gallery_group_members WHERE gid NOT IN (SELECT gid FROM keep_gids);
DELETE FROM eh_galleries          WHERE gid NOT IN (SELECT gid FROM keep_gids);
COMMIT;

VACUUM ANALYZE;
SQL
fi

# ── 5. Extract sampled thumbs ───────────────────────────────────────────────
echo "==> extracting thumbs for sampled gids"
rm -rf thumbs
mkdir -p thumbs

GID_LIST=$(mktemp)
psql "$PG" -tA -c "SELECT './' || gid FROM eh_galleries ORDER BY gid" > "$GID_LIST"
COUNT=$(wc -l < "$GID_LIST" | tr -d ' ')
echo "    extracting $COUNT files…"

# Single pass through the tar — much faster than one-extract-per-file.
# `|| true` because some sampled gids may not have a thumb in the archive.
tar xf "$TAR" -C thumbs --files-from="$GID_LIST" 2>/dev/null || true
rm "$GID_LIST"

EXTRACTED=$(find thumbs -maxdepth 1 -type f | wc -l | tr -d ' ')
THUMB_BYTES=$(du -sh thumbs | cut -f1)
echo "    extracted $EXTRACTED / $COUNT thumbs ($THUMB_BYTES)"

# ── 6. Start caddy ──────────────────────────────────────────────────────────
echo "==> starting thumb server"
docker compose up -d thumbs

# ── 7. Summary ──────────────────────────────────────────────────────────────
echo
echo "==> demo ready"
psql "$PG" -c "
  SELECT 'eh_galleries'          AS tbl, COUNT(*) FROM eh_galleries
  UNION ALL
  SELECT 'gallery_group_members',       COUNT(*) FROM gallery_group_members;
"
cat <<EOF
Postgres : postgresql://postgres:postgres@localhost:5433/eh_stash
Thumbs   : http://localhost:8080/<gid>

Next steps:
  1. (cd worker && npm install && npm run dev)
  2. (cd frontend && npm install && npm run dev)
  3. open http://localhost:5173

To wipe & rebuild:  ./demo/reset.sh && ./demo/setup.sh
EOF
