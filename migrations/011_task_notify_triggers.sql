-- LISTEN/NOTIFY wake-ups replacing fixed 5s polls:
--   task_action  → scraper manager loop picks up requested_action instantly
--   task_events  → API SSE stream forwards new timeline events instantly
-- Both consumers keep a poll fallback, so these triggers are latency
-- optimizations, not correctness requirements.

CREATE OR REPLACE FUNCTION notify_task_action() RETURNS trigger AS $$
BEGIN
    IF NEW.requested_action IS NOT NULL THEN
        PERFORM pg_notify('task_action', NEW.id::text);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_task_action_notify ON sync_task_defs;
CREATE TRIGGER trg_task_action_notify
    AFTER UPDATE OF requested_action ON sync_task_defs
    FOR EACH ROW
    EXECUTE FUNCTION notify_task_action();

CREATE OR REPLACE FUNCTION notify_task_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('task_events', NEW.id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_task_event_notify ON sync_task_events;
CREATE TRIGGER trg_task_event_notify
    AFTER INSERT ON sync_task_events
    FOR EACH ROW
    EXECUTE FUNCTION notify_task_event();
