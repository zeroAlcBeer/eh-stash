-- Live process state for the Pi -> Neon + R2 sync worker.
--
-- This singleton row is updated only in the Pi PostgreSQL database. The
-- heartbeat therefore remains observable without waking Neon.

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
