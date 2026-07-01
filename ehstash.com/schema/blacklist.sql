-- blacklist.sql
-- Run once after `import-data.sh` to physically remove galleries matching
-- the blacklist. Keep this list in sync with TAG_BLACKLIST in
-- worker/src/index.ts — the worker filter is the runtime fallback, this
-- script is the one-shot data scrub.
--
-- gallery_group_members rows are removed automatically via ON DELETE
-- CASCADE on the FK.
--
-- Usage:
--   psql "$NEON_URL" -f schema/blacklist.sql

BEGIN;

-- Single-tag rules
DELETE FROM eh_galleries WHERE tags @> '{"male":["yaoi"]}'::jsonb;
DELETE FROM eh_galleries WHERE tags @> '{"female":["amputee"]}'::jsonb;
DELETE FROM eh_galleries WHERE tags @> '{"female":["futanari"]}'::jsonb;
DELETE FROM eh_galleries WHERE tags @> '{"other":["full color"]}'::jsonb;

-- Composite rule: female:pregnant AND female:dark nipples both present
DELETE FROM eh_galleries
WHERE tags @> '{"female":["pregnant"]}'::jsonb
  AND tags @> '{"female":["dark nipples"]}'::jsonb;

SELECT 'eh_galleries' AS tbl, COUNT(*) AS remaining FROM eh_galleries
UNION ALL
SELECT 'gallery_group_members', COUNT(*) FROM gallery_group_members;

COMMIT;
