-- Wake Admin SSE subscribers whenever the singleton runtime row changes.
--
-- pi-sync updates this row at cycle boundaries and on every heartbeat, so one
-- lightweight PostgreSQL notification is enough to refresh the full status
-- snapshot without polling.

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
