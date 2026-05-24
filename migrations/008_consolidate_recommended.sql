-- 008_consolidate_recommended.sql
--
-- Consolidate all derived/computed gallery state into recommended_cache:
--   - similarity     (cosine sim against user_profile)
--   - tag_embedding  (moved here from eh_galleries; eh_galleries is kept as
--                     source-only — see migration 009 which finally drops the
--                     legacy column after the new code is verified)
--   - rec_score      (legacy TF-IDF score; retained nullable, no longer written)
--
-- This migration is additive: the prod-running old code still reads/writes
-- eh_galleries.tag_embedding and continues to work. After the new code is
-- deployed and verified, run 009 to drop the legacy column.

BEGIN;

ALTER TABLE recommended_cache ALTER COLUMN rec_score DROP NOT NULL;
ALTER TABLE recommended_cache ADD COLUMN IF NOT EXISTS similarity REAL;
ALTER TABLE recommended_cache ADD COLUMN IF NOT EXISTS tag_embedding sparsevec(65536);
ALTER TABLE recommended_cache ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Ensure every active gallery has a recommended_cache row.
INSERT INTO recommended_cache (gid)
SELECT gid FROM eh_galleries WHERE is_active = TRUE
ON CONFLICT (gid) DO NOTHING;

-- Copy existing tag_embedding data from eh_galleries (idempotent).
UPDATE recommended_cache rc
SET tag_embedding = g.tag_embedding, updated_at = NOW()
FROM eh_galleries g
WHERE rc.gid = g.gid
  AND g.tag_embedding IS NOT NULL
  AND rc.tag_embedding IS NULL;

CREATE INDEX IF NOT EXISTS idx_recommended_cache_embedding_pending
    ON recommended_cache (gid)
    WHERE tag_embedding IS NULL;

CREATE INDEX IF NOT EXISTS idx_recommended_cache_similarity
    ON recommended_cache (similarity DESC)
    WHERE similarity IS NOT NULL;

-- Initial similarity backfill using the current user_profile (without rebuilding
-- profile — that's a separate operation done by the worker after deploy).
UPDATE recommended_cache rc
SET similarity = NULLIF(1 - (rc.tag_embedding <=> up.embedding), 'NaN'::float8),
    updated_at = NOW()
FROM user_profile up
WHERE up.id = 1 AND up.embedding IS NOT NULL AND rc.tag_embedding IS NOT NULL;

COMMIT;
