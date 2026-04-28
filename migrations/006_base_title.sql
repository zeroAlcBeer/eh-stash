-- 006_base_title.sql
-- Materialize normalized base_title for efficient gallery grouping.

ALTER TABLE eh_galleries ADD COLUMN IF NOT EXISTS base_title TEXT;

-- Backfill existing rows (prefer title_jpn, fallback to title)
-- Strip: [中国翻訳] [中国語] [DL版] [無修正] (C\d+) + whitespace
UPDATE eh_galleries
SET base_title = REGEXP_REPLACE(
    REGEXP_REPLACE(
        COALESCE(NULLIF(title_jpn, ''), title),
        '\s*\[中国翻訳\]|\s*\[中国語\]|\s*\[DL版\]|\s*\[無修正\]|\s*\(C\d+\)', '', 'g'
    ),
    '\s+', '', 'g'
)
WHERE COALESCE(NULLIF(title_jpn, ''), title) IS NOT NULL;

-- Index for grouping lookups
CREATE INDEX IF NOT EXISTS idx_eh_galleries_base_title ON eh_galleries (base_title) WHERE base_title IS NOT NULL AND base_title != '';
