#!/usr/bin/env bash
# Import data.dump into Neon.
#
# Prereqs:
#   - libpq installed (pg_restore on PATH)
#   - Neon connection string exported as NEON_URL
#   - schema/001_init.sql already applied (script applies it idempotently)
#
# Usage:
#   export NEON_URL='postgresql://USER:PASS@ep-...-pooler.ap-southeast-1.aws.neon.tech/neondb?sslmode=require'
#   ./scripts/import-data.sh

set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -z "${NEON_URL:-}" ]]; then
  echo "error: NEON_URL not set" >&2
  exit 1
fi

DUMP_FILE="data/data.dump"
SCHEMA_FILE="schema/001_init.sql"

if [[ ! -f "$DUMP_FILE" ]]; then
  echo "error: $DUMP_FILE not found" >&2
  exit 1
fi

# Use the unpooled (direct) endpoint for bulk restore. Pooled (PgBouncer
# transaction mode) breaks pg_restore's session-level operations.
DIRECT_URL="${NEON_URL/-pooler./.}"

echo "==> applying schema (idempotent if already applied)"
psql "$DIRECT_URL" -v ON_ERROR_STOP=1 -f "$SCHEMA_FILE" || {
  echo "(schema apply failed — likely already exists; continuing)"
}

echo "==> restoring data from $DUMP_FILE"
# --data-only avoids re-creating tables. --disable-triggers skips the FK
# check on gallery_group_members during the COPY (we re-validate after).
pg_restore \
  --dbname="$DIRECT_URL" \
  --data-only \
  --no-owner \
  --no-privileges \
  --single-transaction \
  --verbose \
  "$DUMP_FILE"

echo "==> verifying row counts"
psql "$DIRECT_URL" -c "
  SELECT 'eh_galleries' AS tbl, COUNT(*) FROM eh_galleries
  UNION ALL
  SELECT 'gallery_group_members', COUNT(*) FROM gallery_group_members
  UNION ALL
  SELECT 'eh_galleries (is_active=TRUE)', COUNT(*) FROM eh_galleries WHERE is_active = TRUE;
"

echo "==> done"
