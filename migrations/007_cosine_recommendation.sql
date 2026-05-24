-- 007_cosine_recommendation.sql
-- Cosine vector recommendation: sparse tag embeddings via pgvector.
--
-- Requires the postgres image to be switched to pgvector/pgvector:pg16
-- (or any pg image that bundles the vector extension).
--
-- After this migration runs successfully and the new flow is verified,
-- the legacy TF-IDF tables can be dropped manually:
--     DROP TABLE recommended_cache;
--     DROP TABLE preference_tags;

CREATE EXTENSION IF NOT EXISTS vector;

-- Tag vocabulary: maps each qualifying (namespace, tag) pair to a stable dim.
-- dim is allocated monotonically and never reused. Tags that fall below the
-- count threshold get is_active = FALSE but keep their dim ("dead dim"),
-- which costs nothing in sparse storage and avoids recomputing every gallery.
CREATE TABLE IF NOT EXISTS tag_vocabulary (
    dim         INTEGER PRIMARY KEY,
    namespace   TEXT NOT NULL,
    tag         TEXT NOT NULL,
    idf         DOUBLE PRECISION NOT NULL,
    type_weight DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(namespace, tag)
);

CREATE INDEX IF NOT EXISTS idx_tag_vocabulary_active
    ON tag_vocabulary(dim)
    WHERE is_active = TRUE;

-- Vocabulary metadata: tracks the next-free dim and the gallery-count snapshot
-- used as the IDF base. Single row, id = 1.
CREATE TABLE IF NOT EXISTS tag_vocabulary_meta (
    id              INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    dim_count       INTEGER NOT NULL DEFAULT 0,
    active_count    INTEGER NOT NULL DEFAULT 0,
    total_galleries BIGINT  NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO tag_vocabulary_meta (id) VALUES (1)
    ON CONFLICT (id) DO NOTHING;

-- Per-gallery sparse tag embedding. NULL until the embedding worker fills it.
-- Dim 65536 is a generous upper bound; sparsevec stores only non-zero entries
-- so the cap has effectively zero storage cost.
ALTER TABLE eh_galleries
    ADD COLUMN IF NOT EXISTS tag_embedding sparsevec(65536);

CREATE INDEX IF NOT EXISTS idx_eh_galleries_embedding_pending
    ON eh_galleries(gid)
    WHERE is_active = TRUE AND tag_embedding IS NULL;

-- User profile vector (single-user). Recomputed from scratch on favorites
-- change to avoid floating-point drift.
CREATE TABLE IF NOT EXISTS user_profile (
    id          INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    embedding   sparsevec(65536),
    liked_count INTEGER NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO user_profile (id, liked_count) VALUES (1, 0)
    ON CONFLICT (id) DO NOTHING;

-- App settings: tunable knobs that need to survive restarts.
CREATE TABLE IF NOT EXISTS app_settings (
    key   TEXT PRIMARY KEY,
    value JSONB NOT NULL
);

INSERT INTO app_settings (key, value) VALUES
    ('similarity_threshold', '0.3'::jsonb)
ON CONFLICT (key) DO NOTHING;
