-- Structured run history for the Pi -> Neon + R2 sync worker.
--
-- Records live in the Pi PostgreSQL database so observing sync health never
-- wakes Neon. A row is inserted before connecting to Neon and finalized after
-- the cycle succeeds or fails.

CREATE TABLE IF NOT EXISTS cloud_sync_runs (
    id                       BIGSERIAL PRIMARY KEY,
    trigger                  TEXT NOT NULL DEFAULT 'scheduled',
    cadence_sec              INTEGER NOT NULL CHECK (cadence_sec > 0),
    status                   TEXT NOT NULL DEFAULT 'running'
                             CHECK (status IN ('running', 'succeeded', 'failed')),
    started_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at              TIMESTAMPTZ,
    duration_ms              BIGINT CHECK (duration_ms IS NULL OR duration_ms >= 0),
    backlog_before           INTEGER NOT NULL DEFAULT 0 CHECK (backlog_before >= 0),
    backlog_after            INTEGER CHECK (backlog_after IS NULL OR backlog_after >= 0),
    oldest_pending_before    TIMESTAMPTZ,
    oldest_pending_after     TIMESTAMPTZ,
    selected_count           INTEGER NOT NULL DEFAULT 0 CHECK (selected_count >= 0),
    pushed_count             INTEGER NOT NULL DEFAULT 0 CHECK (pushed_count >= 0),
    no_file_count            INTEGER NOT NULL DEFAULT 0 CHECK (no_file_count >= 0),
    r2_error_count           INTEGER NOT NULL DEFAULT 0 CHECK (r2_error_count >= 0),
    kept_count               INTEGER NOT NULL DEFAULT 0 CHECK (kept_count >= 0),
    group_affected           INTEGER,
    error_kind               TEXT,
    error_message            TEXT,
    CHECK (finished_at IS NULL OR finished_at >= started_at)
);

CREATE INDEX IF NOT EXISTS idx_cloud_sync_runs_started_at
    ON cloud_sync_runs (started_at DESC);

CREATE INDEX IF NOT EXISTS idx_cloud_sync_runs_status_started
    ON cloud_sync_runs (status, started_at DESC);
