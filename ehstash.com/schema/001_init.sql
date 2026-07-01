-- 001_init.sql
-- ehstash.com read-only schema for Neon.
--
-- Matches the source dump's column shape so `pg_restore` can apply data without
-- column-list mismatches. Columns the worker doesn't use (is_active,
-- last_synced_at) are kept for restore compatibility — they cost nothing.
--
-- row_updated_at is a new column not present in the source dump. The source
-- COPY statement lists explicit columns, so existing rows take the DEFAULT
-- (NOW() at import time). It exists as the watermark for a future
-- Pi-to-Neon incremental sync pipeline.

CREATE TABLE eh_galleries (
    gid             BIGINT PRIMARY KEY,
    token           TEXT NOT NULL,
    category        TEXT,
    title           TEXT,
    title_jpn       TEXT,
    uploader        TEXT,
    posted_at       TIMESTAMPTZ,
    language        TEXT,
    pages           INT,
    rating          NUMERIC(3, 2),
    fav_count       INT DEFAULT 0,
    thumb           TEXT,
    comment_count   INT DEFAULT 0,
    tags            JSONB,
    last_synced_at  TIMESTAMPTZ,
    is_active       BOOLEAN DEFAULT TRUE,
    base_title      TEXT,
    row_updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_eh_galleries_category   ON eh_galleries (category);
CREATE INDEX idx_eh_galleries_fav_count  ON eh_galleries (fav_count DESC);
CREATE INDEX idx_eh_galleries_rating     ON eh_galleries (rating DESC);
CREATE INDEX idx_eh_galleries_posted_at  ON eh_galleries (posted_at DESC);
CREATE INDEX idx_eh_galleries_comment    ON eh_galleries (comment_count DESC);
CREATE INDEX idx_eh_galleries_language   ON eh_galleries (language);
CREATE INDEX idx_eh_galleries_tags       ON eh_galleries USING GIN (tags);
CREATE INDEX idx_eh_galleries_base_title ON eh_galleries (base_title)
    WHERE base_title IS NOT NULL AND base_title <> '';
CREATE INDEX idx_eh_galleries_row_updated ON eh_galleries (row_updated_at);
CREATE INDEX idx_eh_galleries_active     ON eh_galleries (gid)
    WHERE is_active = TRUE;

CREATE TABLE gallery_group_members (
    group_id BIGINT NOT NULL,
    gid      BIGINT NOT NULL UNIQUE REFERENCES eh_galleries(gid) ON DELETE CASCADE
);

CREATE INDEX idx_ggm_group_id ON gallery_group_members (group_id);
