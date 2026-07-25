-- Per-task timeline queries (GET /v1/admin/tasks/{id}/events) page by
-- task_id + id DESC; the existing (created_at, id) index doesn't cover that.
CREATE INDEX IF NOT EXISTS idx_sync_task_events_task_id
    ON sync_task_events (task_id, id DESC);
