-- 009_pi_sync.sql
-- Pi → Neon + R2 sync infrastructure (pi-sync container).
--
-- sync_state:
--   Single-row table. cursor_gid drives the rotating backfill chunk; once a
--   full rotation completes without producing diffs, caught_up flips TRUE
--   and pi-sync drops the backfill phase, running outbox-only.
--
-- sync_outbox:
--   Written by scraper-go's UpsertGalleriesBulk in the same tx as every
--   gallery upsert. PK on gid coalesces repeated upserts of the same gallery
--   (only the latest enqueued_at survives). pi-sync drains rows via an
--   optimistic conditional DELETE using enqueued_at to detect concurrent
--   re-enqueues.

CREATE TABLE IF NOT EXISTS sync_state (
    id                    INT PRIMARY KEY,
    cursor_gid            BIGINT,
    caught_up             BOOLEAN NOT NULL DEFAULT FALSE,
    rotation_had_changes  BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO sync_state (id, cursor_gid) VALUES (1, NULL)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS sync_outbox (
    gid          BIGINT PRIMARY KEY,
    enqueued_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sync_outbox_enqueued
    ON sync_outbox (enqueued_at);
