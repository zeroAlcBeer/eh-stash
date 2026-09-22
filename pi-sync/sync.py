"""
Pi → R2 thumbnail sync.

ehstash.com is retired; the only remaining cloud path is the R2 thumbnail
bucket, which is the source of truth for thumbs (the frontend fetches them
straight from the CDN via VITE_THUMB_BASE_URL).

Two channels of work per cycle:

  1. sync_outbox drain (always — the steady-state path).
     scraper-go inserts a row into sync_outbox in the same tx as every
     UpsertGalleriesBulk. We claim by reading (gid, enqueued_at):
       - local thumb file present → R2 PUT → delete local file → outbox delete
       - no local file but the object already exists on R2 → outbox delete
       - neither → keep the row; the thumb may land in a later scrape
     The conditional DELETE ... WHERE gid AND enqueued_at drops the row only
     when nothing newer was enqueued in the meantime; otherwise the row
     survives for the next cycle.

  2. Local backfill sweep.
     A leftover file in THUMB_DIR means its gid was never uploaded (or the
     post-upload delete failed), so the directory itself is the queue — no
     DB state needed. Each cycle uploads up to SYNC_CHUNK_ROT leftover files
     and deletes them locally on success.
"""

import logging
import os
import signal
import threading
import time
from dataclasses import dataclass
from pathlib import Path

import boto3
import psycopg2
from botocore.exceptions import ClientError

# ─── Config ─────────────────────────────────────────────────────────────────

PI_DSN          = os.environ["PI_DSN"]
R2_ENDPOINT     = os.environ["R2_ENDPOINT"]
R2_BUCKET       = os.environ["R2_BUCKET"]
R2_KEY_ID       = os.environ["R2_ACCESS_KEY_ID"]
R2_SECRET       = os.environ["R2_SECRET_ACCESS_KEY"]
THUMB_DIR       = Path(os.environ.get("THUMB_DIR", "/data/thumbs"))
CADENCE_SEC     = int(os.environ.get("SYNC_CADENCE_SEC", "300"))
HEARTBEAT_SEC   = max(1, int(os.environ.get("SYNC_HEARTBEAT_SEC", "60")))
BACKFILL_BATCH  = int(os.environ.get("SYNC_CHUNK_ROT", "5000"))
OUTBOX_BATCH    = int(os.environ.get("SYNC_OUTBOX_BATCH", "500"))
ONESHOT         = os.environ.get("SYNC_ONESHOT") == "1"
HEALTH_PORT     = int(os.environ.get("SYNC_HEALTH_PORT", "8097"))

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("pi-sync")

# ─── Lifecycle ──────────────────────────────────────────────────────────────

_stopping = False

def _stop(*_):
    global _stopping
    _stopping = True
    log.info("shutdown signal received, will exit after current cycle")

signal.signal(signal.SIGTERM, _stop)
signal.signal(signal.SIGINT,  _stop)


def _interruptible_sleep(seconds: int):
    deadline = time.monotonic() + seconds
    while not _stopping and time.monotonic() < deadline:
        time.sleep(min(1.0, deadline - time.monotonic()))


# ─── R2 ─────────────────────────────────────────────────────────────────────

r2 = boto3.client(
    "s3",
    endpoint_url=R2_ENDPOINT,
    aws_access_key_id=R2_KEY_ID,
    aws_secret_access_key=R2_SECRET,
)


def r2_put_thumb(gid: int) -> str:
    """'ok' | 'no_file' | 'error'"""
    path = THUMB_DIR / str(gid)
    if not path.exists():
        return "no_file"
    try:
        with path.open("rb") as f:
            r2.put_object(
                Bucket=R2_BUCKET,
                Key=str(gid),
                Body=f,
                ContentType="image/jpeg",
            )
        return "ok"
    except ClientError as e:
        log.warning("gid=%d R2 PUT failed: %s", gid, e)
        return "error"


def r2_thumb_exists(gid: int) -> bool:
    try:
        r2.head_object(Bucket=R2_BUCKET, Key=str(gid))
        return True
    except ClientError as e:
        code = e.response.get("Error", {}).get("Code", "")
        if code in ("404", "NoSuchKey", "NotFound"):
            return False
        raise


def delete_local_thumb(gid: int):
    """Remove the local thumb after a successful R2 PUT — R2 is the source
    of truth. Non-fatal: a failed delete just leaves a stale file that'll be
    reaped on the next backfill pass."""
    path = THUMB_DIR / str(gid)
    try:
        path.unlink()
    except FileNotFoundError:
        pass
    except OSError as e:
        log.warning("gid=%d local thumb delete failed: %s", gid, e)


# ─── Pi helpers ─────────────────────────────────────────────────────────────

def pi_outbox_peek(pi_conn, limit):
    with pi_conn.cursor() as cur:
        cur.execute(
            "SELECT gid, enqueued_at FROM sync_outbox "
            "ORDER BY enqueued_at LIMIT %s",
            (limit,),
        )
        return cur.fetchall()


def pi_outbox_stats(pi_conn):
    with pi_conn.cursor() as cur:
        cur.execute("SELECT COUNT(*)::int, MIN(enqueued_at) FROM sync_outbox")
        return cur.fetchone()


def pi_outbox_delete_if_unchanged(pi_conn, gid, enqueued_at):
    """Returns True if the row was deleted (no concurrent re-enqueue)."""
    with pi_conn.cursor() as cur:
        cur.execute(
            "DELETE FROM sync_outbox WHERE gid = %s AND enqueued_at = %s",
            (gid, enqueued_at),
        )
        deleted = cur.rowcount
    pi_conn.commit()
    return deleted > 0


# ─── Run history ────────────────────────────────────────────────────────────

@dataclass
class CycleStats:
    selected: int = 0
    pushed: int = 0
    no_file: int = 0
    r2_error: int = 0
    kept: int = 0


def classify_error(error):
    message = str(error).lower()
    if "quota" in message:
        return "quota"
    if "password authentication failed" in message or "authentication" in message:
        return "authentication"
    if isinstance(error, psycopg2.OperationalError):
        return "network"
    if isinstance(error, ClientError):
        return "r2"
    return "unknown"


def pi_start_run(pi_conn, trigger):
    backlog, oldest = pi_outbox_stats(pi_conn)
    with pi_conn.cursor() as cur:
        cur.execute(
            """
            INSERT INTO cloud_sync_runs (
                trigger, cadence_sec, backlog_before, oldest_pending_before
            )
            VALUES (%s, %s, %s, %s)
            RETURNING id
            """,
            (trigger, CADENCE_SEC, backlog, oldest),
        )
        run_id = cur.fetchone()[0]
    pi_conn.commit()
    return run_id


def pi_finish_run(pi_conn, run_id, status, duration_ms, stats=None, error=None):
    stats = stats or CycleStats()
    try:
        pi_conn.rollback()
        backlog, oldest = pi_outbox_stats(pi_conn)
        error_kind = classify_error(error) if error is not None else None
        error_message = str(error)[:4000] if error is not None else None
        with pi_conn.cursor() as cur:
            cur.execute(
                """
                UPDATE cloud_sync_runs
                SET status = %s,
                    finished_at = NOW(),
                    duration_ms = %s,
                    backlog_after = %s,
                    oldest_pending_after = %s,
                    selected_count = %s,
                    pushed_count = %s,
                    no_file_count = %s,
                    r2_error_count = %s,
                    kept_count = %s,
                    group_affected = NULL,
                    error_kind = %s,
                    error_message = %s
                WHERE id = %s
                """,
                (
                    status, duration_ms, backlog, oldest,
                    stats.selected, stats.pushed, stats.no_file,
                    stats.r2_error, stats.kept,
                    error_kind, error_message, run_id,
                ),
            )
        pi_conn.commit()
    except Exception:
        log.exception("failed to finalize cloud sync run id=%s", run_id)


# ─── Runtime state & heartbeat ──────────────────────────────────────────────

def pi_runtime_started(pi_conn):
    with pi_conn.cursor() as cur:
        cur.execute(
            """
            UPDATE cloud_sync_runtime
            SET phase = 'starting',
                worker_started_at = NOW(),
                heartbeat_at = NOW(),
                cycle_started_at = NULL,
                next_run_at = NULL,
                current_run_id = NULL,
                cadence_sec = %s,
                updated_at = NOW()
            WHERE id = 1
            """,
            (CADENCE_SEC,),
        )
    pi_conn.commit()


def pi_runtime_cycle_started(pi_conn, run_id):
    with pi_conn.cursor() as cur:
        cur.execute(
            """
            UPDATE cloud_sync_runtime
            SET phase = 'running',
                heartbeat_at = NOW(),
                cycle_started_at = NOW(),
                next_run_at = NULL,
                current_run_id = %s,
                cadence_sec = %s,
                updated_at = NOW()
            WHERE id = 1
            """,
            (run_id, CADENCE_SEC),
        )
    pi_conn.commit()


def pi_runtime_cycle_finished(pi_conn, run_id, succeeded, error=None):
    error_kind = classify_error(error) if error is not None else None
    error_message = str(error)[:4000] if error is not None else None
    with pi_conn.cursor() as cur:
        cur.execute(
            """
            UPDATE cloud_sync_runtime
            SET phase = %s,
                heartbeat_at = NOW(),
                cycle_started_at = NULL,
                next_run_at = NOW() + (%s * INTERVAL '1 second'),
                current_run_id = NULL,
                last_run_id = COALESCE(%s, last_run_id),
                last_success_at = CASE WHEN %s THEN NOW() ELSE last_success_at END,
                consecutive_failures = CASE
                    WHEN %s THEN 0
                    ELSE consecutive_failures + 1
                END,
                last_error_kind = %s,
                last_error_message = %s,
                updated_at = NOW()
            WHERE id = 1
            """,
            (
                "sleeping" if succeeded else "failed",
                CADENCE_SEC,
                run_id,
                succeeded,
                succeeded,
                error_kind,
                error_message,
            ),
        )
    pi_conn.commit()


def pi_runtime_stopped(pi_conn):
    with pi_conn.cursor() as cur:
        cur.execute(
            """
            UPDATE cloud_sync_runtime
            SET phase = 'stopped',
                heartbeat_at = NOW(),
                cycle_started_at = NULL,
                next_run_at = NULL,
                current_run_id = NULL,
                updated_at = NOW()
            WHERE id = 1
            """
        )
    pi_conn.commit()


def safe_runtime_update(label, update, *args, **kwargs):
    try:
        update(*args, **kwargs)
    except Exception as error:
        log.warning("runtime %s update failed: %s", label, error)


class RuntimeHeartbeat:
    def __init__(self):
        self._stop_event = threading.Event()
        self._thread = threading.Thread(
            target=self._run,
            name="cloud-sync-heartbeat",
            daemon=True,
        )

    def start(self):
        self._thread.start()

    def stop(self):
        self._stop_event.set()
        self._thread.join(timeout=5)

    def _run(self):
        while not self._stop_event.is_set():
            conn = None
            try:
                conn = psycopg2.connect(PI_DSN)
                with conn.cursor() as cur:
                    cur.execute(
                        """
                        UPDATE cloud_sync_runtime
                        SET heartbeat_at = NOW(), updated_at = NOW()
                        WHERE id = 1
                        """
                    )
                conn.commit()
            except Exception as error:
                log.warning("runtime heartbeat failed: %s", error)
            finally:
                if conn is not None:
                    try:
                        conn.close()
                    except Exception:
                        pass
            self._stop_event.wait(HEARTBEAT_SEC)


# ─── Phase 1: outbox drain ──────────────────────────────────────────────────

def drain_outbox(pi_conn):
    """Returns (selected, pushed, no_file, r2_err, kept_due_to_race)."""
    rows = pi_outbox_peek(pi_conn, OUTBOX_BATCH)
    if not rows:
        return (0, 0, 0, 0, 0)

    pushed = no_file = r2_err = kept = 0
    for gid, enq in rows:
        result = r2_put_thumb(gid)
        if result == "ok":
            delete_local_thumb(gid)
        elif result == "error":
            r2_err += 1
            continue
        else:
            # No local file: either already uploaded in a previous cycle
            # (R2 is source of truth → drop the row) or the scraper hasn't
            # fetched the thumb yet (keep the row for a later cycle).
            try:
                if not r2_thumb_exists(gid):
                    no_file += 1
                    continue
            except ClientError as e:
                log.warning("gid=%d R2 HEAD failed: %s", gid, e)
                r2_err += 1
                continue
        if pi_outbox_delete_if_unchanged(pi_conn, gid, enq):
            pushed += 1
        else:
            kept += 1

    return (len(rows), pushed, no_file, r2_err, kept)


# ─── Phase 2: local backfill sweep ──────────────────────────────────────────

def backfill_local_thumbs(limit):
    """Upload leftover local thumbs to R2 and delete them locally.

    A file remaining in THUMB_DIR means its gid was never uploaded (or the
    post-upload delete failed) — the directory itself is the queue.
    Returns (scanned, uploaded, errors).
    """
    scanned = uploaded = errors = 0
    try:
        entries = os.scandir(THUMB_DIR)
    except OSError as e:
        log.warning("thumb dir scan failed: %s", e)
        return (0, 0, 1)
    with entries:
        for entry in entries:
            if scanned >= limit:
                break
            if not entry.is_file() or not entry.name.isdigit():
                continue
            scanned += 1
            gid = int(entry.name)
            result = r2_put_thumb(gid)
            if result == "ok":
                uploaded += 1
                delete_local_thumb(gid)
            elif result == "error":
                errors += 1
    return (scanned, uploaded, errors)


# ─── Cycle ──────────────────────────────────────────────────────────────────

def run_cycle(pi_conn):
    obx_selected, obx_pushed, obx_no_file, obx_r2_err, obx_kept = drain_outbox(pi_conn)
    bf_scanned, bf_uploaded, bf_errors = backfill_local_thumbs(BACKFILL_BATCH)

    log.info(
        "cycle: outbox(selected=%d pushed=%d no_file=%d r2_err=%d kept=%d) "
        "backfill(scanned=%d uploaded=%d errors=%d)",
        obx_selected, obx_pushed, obx_no_file, obx_r2_err, obx_kept,
        bf_scanned, bf_uploaded, bf_errors,
    )
    return CycleStats(
        selected=obx_selected,
        pushed=obx_pushed + bf_uploaded,
        no_file=obx_no_file,
        r2_error=obx_r2_err + bf_errors,
        kept=obx_kept,
    )


# ─── Main loop ──────────────────────────────────────────────────────────────

def start_health_server():
    """Liveness endpoint on a daemon thread for Uptime Kuma and the deploy
    Verify step. pi-sync exposes no inbound API otherwise; this only signals
    that the process is up (the loop's real health is tracked in the DB via
    the runtime heartbeat)."""
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    class _Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/healthz":
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"status": "ok"}')
            else:
                self.send_response(404)
                self.end_headers()

        def log_message(self, *_args):
            pass  # silence per-request stderr logging

    server = ThreadingHTTPServer(("0.0.0.0", HEALTH_PORT), _Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    log.info("health server listening on :%d", HEALTH_PORT)


def main():
    log.info(
        "pi-sync starting: cadence=%ds heartbeat=%ds backfill_batch=%d "
        "outbox_batch=%d thumbs=%s",
        CADENCE_SEC, HEARTBEAT_SEC, BACKFILL_BATCH, OUTBOX_BATCH, THUMB_DIR,
    )
    start_health_server()
    heartbeat = RuntimeHeartbeat()
    startup_conn = None
    try:
        startup_conn = psycopg2.connect(PI_DSN)
        pi_runtime_started(startup_conn)
    except Exception as error:
        log.warning("failed to initialize runtime state: %s", error)
    finally:
        if startup_conn is not None:
            startup_conn.close()
    heartbeat.start()

    first_cycle = True
    try:
        while not _stopping:
            t0 = time.monotonic()
            pi = None
            run_id = None
            stats = CycleStats()
            try:
                pi = psycopg2.connect(PI_DSN)
                if ONESHOT:
                    trigger = "oneshot"
                elif first_cycle:
                    trigger = "startup"
                else:
                    trigger = "scheduled"
                run_id = pi_start_run(pi, trigger)
                safe_runtime_update(
                    "cycle-started",
                    pi_runtime_cycle_started,
                    pi,
                    run_id,
                )
                stats = run_cycle(pi)
                elapsed_ms = round((time.monotonic() - t0) * 1000)
                pi_finish_run(pi, run_id, "succeeded", elapsed_ms, stats=stats)
                safe_runtime_update(
                    "cycle-finished",
                    pi_runtime_cycle_finished,
                    pi,
                    run_id,
                    succeeded=True,
                )
            except Exception as error:
                log.exception("cycle failed: %s", error)
                if pi is not None:
                    if run_id is not None:
                        elapsed_ms = round((time.monotonic() - t0) * 1000)
                        pi_finish_run(
                            pi,
                            run_id,
                            "failed",
                            elapsed_ms,
                            stats=stats,
                            error=error,
                        )
                    else:
                        pi.rollback()
                    safe_runtime_update(
                        "cycle-failed",
                        pi_runtime_cycle_finished,
                        pi,
                        run_id,
                        succeeded=False,
                        error=error,
                    )
            finally:
                if pi is not None:
                    try:
                        pi.close()
                    except Exception:
                        pass
            first_cycle = False
            elapsed = time.monotonic() - t0
            if _stopping or ONESHOT:
                suffix = " (oneshot, exiting)" if ONESHOT else ""
                log.info("cycle done in %.1fs%s", elapsed, suffix)
                break
            log.info("cycle done in %.1fs, sleeping %ds", elapsed, CADENCE_SEC)
            _interruptible_sleep(CADENCE_SEC)
    finally:
        heartbeat.stop()
        stop_conn = None
        try:
            stop_conn = psycopg2.connect(PI_DSN)
            pi_runtime_stopped(stop_conn)
        except Exception as error:
            log.warning("failed to mark runtime stopped: %s", error)
        finally:
            if stop_conn is not None:
                stop_conn.close()
    log.info("pi-sync stopped")


if __name__ == "__main__":
    main()
