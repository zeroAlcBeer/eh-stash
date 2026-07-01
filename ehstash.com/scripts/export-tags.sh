#!/usr/bin/env bash
# Materialize the active tag whitelist from Neon into worker/src/tags.json.
#
# The worker imports tags.json at module init and rejects any /v1/galleries
# request whose tag= parameter doesn't appear in the set, returning an
# empty page without touching the DB. Run this whenever a data sync
# introduces new tags, then `wrangler deploy`. Stale whitelist means
# legitimate new tags get rejected; empty whitelist disables the gate
# (bootstrap mode).
#
# Usage:
#   export NEON_URL='postgresql://USER:PASS@ep-...-pooler.ap-southeast-1.aws.neon.tech/neondb?sslmode=require'
#   ./scripts/export-tags.sh

set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -z "${NEON_URL:-}" ]]; then
  echo "error: NEON_URL not set" >&2
  exit 1
fi

OUT="worker/src/tags.json"
TMP="$OUT.tmp"

read -r -d '' TAGS_SQL <<'SQL' || true
SELECT jsonb_build_object(
  'version', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
  'tags',    COALESCE(jsonb_agg(DISTINCT t ORDER BY t), '[]'::jsonb)
)
FROM (
  SELECT lower(trim(kv.key)) || ':' || trim(val) AS t
  FROM eh_galleries g,
       jsonb_each(g.tags) kv,
       jsonb_array_elements_text(kv.value) val
  WHERE g.is_active = TRUE
    AND g.tags IS NOT NULL
) s
WHERE t IS NOT NULL AND t <> ':';
SQL

echo "==> querying tag set from Neon"
psql "$NEON_URL" -At -c "$TAGS_SQL" > "$TMP"

# Validate JSON before swapping in — bad output should not blow away the
# previous working file.
node -e "JSON.parse(require('fs').readFileSync('$TMP','utf8'))" \
  || { echo "error: psql output is not valid JSON" >&2; rm -f "$TMP"; exit 1; }

mv "$TMP" "$OUT"

COUNT=$(node -e "console.log(JSON.parse(require('fs').readFileSync('$OUT','utf8')).tags.length)")
echo "==> wrote $OUT with $COUNT tags"
echo "==> next: (cd worker && wrangler deploy)"
