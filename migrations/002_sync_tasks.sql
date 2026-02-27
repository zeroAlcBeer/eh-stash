-- 002_sync_tasks.sql

-- 同步任务表（替代 schedule_state 的角色）
CREATE TABLE IF NOT EXISTS sync_tasks (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    type            TEXT NOT NULL CHECK (type IN ('full', 'incremental')),
    category        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'stopped'
                    CHECK (status IN ('stopped', 'running', 'completed', 'error')),
    config          JSONB NOT NULL DEFAULT '{}',
    state           JSONB NOT NULL DEFAULT '{}',
    progress_pct    REAL DEFAULT 0,
    desired_status  TEXT NOT NULL DEFAULT 'stopped'
                    CHECK (desired_status IN ('running', 'stopped')),
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    last_run_at     TIMESTAMPTZ,
    error_message   TEXT
);

-- 缩略图队列表（替代磁盘扫描 diff）
CREATE TABLE IF NOT EXISTS thumb_queue (
    id              SERIAL PRIMARY KEY,
    gid             BIGINT NOT NULL UNIQUE,
    thumb_url       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    retry_count     INT DEFAULT 0,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    processed_at    TIMESTAMPTZ,
    next_retry_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_thumb_queue_pending
    ON thumb_queue (created_at) WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_thumb_queue_retry
    ON thumb_queue (next_retry_at) WHERE status = 'pending';
