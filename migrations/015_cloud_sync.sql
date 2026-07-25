-- Pi -> Neon + R2 cloud sync infrastructure and observability.
--
-- This migration intentionally consolidates the former 004, 012, 013, and
-- 014 files. Every statement is idempotent so existing databases converge
-- safely while new databases create the complete cloud sync schema at once.

-- Rotating backfill state. Once a full rotation produces no differences,
-- caught_up flips TRUE and pi-sync runs outbox-only.
CREATE TABLE IF NOT EXISTS sync_state (
    id                    INT PRIMARY KEY,
    cursor_gid            BIGINT,
    caught_up             BOOLEAN NOT NULL DEFAULT FALSE,
    rotation_had_changes  BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO sync_state (id, cursor_gid)
VALUES (1, NULL)
ON CONFLICT (id) DO NOTHING;

-- Coalescing queue written by scraper-go in the same transaction as gallery
-- upserts and drained by pi-sync.
CREATE TABLE IF NOT EXISTS sync_outbox (
    gid          BIGINT PRIMARY KEY,
    enqueued_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sync_outbox_enqueued
    ON sync_outbox (enqueued_at);

-- Structured history for every cloud export cycle.
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

-- Singleton live state. It stays in the Pi database, so observing worker
-- health never wakes Neon.
CREATE TABLE IF NOT EXISTS cloud_sync_runtime (
    id                    SMALLINT PRIMARY KEY CHECK (id = 1),
    phase                 TEXT NOT NULL
                          CHECK (phase IN (
                              'starting', 'running', 'sleeping', 'failed', 'stopped'
                          )),
    worker_started_at     TIMESTAMPTZ,
    heartbeat_at          TIMESTAMPTZ,
    cycle_started_at      TIMESTAMPTZ,
    last_success_at       TIMESTAMPTZ,
    next_run_at           TIMESTAMPTZ,
    current_run_id        BIGINT REFERENCES cloud_sync_runs(id) ON DELETE SET NULL,
    last_run_id           BIGINT REFERENCES cloud_sync_runs(id) ON DELETE SET NULL,
    cadence_sec           INTEGER NOT NULL CHECK (cadence_sec > 0),
    consecutive_failures  INTEGER NOT NULL DEFAULT 0
                          CHECK (consecutive_failures >= 0),
    last_error_kind       TEXT,
    last_error_message    TEXT,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO cloud_sync_runtime (id, phase, cadence_sec)
VALUES (1, 'stopped', 21600)
ON CONFLICT (id) DO NOTHING;

-- Wake Admin SSE subscribers on cycle boundaries and heartbeat updates.
CREATE OR REPLACE FUNCTION notify_cloud_sync_runtime_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify(
        'cloud_sync_runtime',
        json_build_object(
            'phase', NEW.phase,
            'heartbeat_at', NEW.heartbeat_at,
            'current_run_id', NEW.current_run_id,
            'last_run_id', NEW.last_run_id,
            'updated_at', NEW.updated_at
        )::text
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_cloud_sync_runtime_notify
    ON cloud_sync_runtime;

CREATE TRIGGER trg_cloud_sync_runtime_notify
AFTER INSERT OR UPDATE ON cloud_sync_runtime
FOR EACH ROW
EXECUTE FUNCTION notify_cloud_sync_runtime_change();
