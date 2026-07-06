"""
Pi → Neon + R2 sync.

Two channels of work per cycle:

  1. sync_outbox drain (always — the steady-state path).
     scraper-go inserts a row into sync_outbox in the same tx as every
     UpsertGalleriesBulk. We claim by reading (gid, enqueued_at), do the
     R2 PUT + Neon UPSERT, then DELETE WHERE gid = ? AND enqueued_at = ?.
     The conditional DELETE drops the row only when nothing newer was
     enqueued in the meantime; otherwise the row survives for next cycle.

  2. Rotating backfill chunk (only while sync_state.caught_up = FALSE).
     A single sliding window of SYNC_CHUNK_ROT gids moves down by gid DESC
     each cycle, wrapping when it hits the bottom of the table. We diff Pi
     vs Neon on (fav_count, is_active), push new + changed via the same
     R2/Neon path. If a full rotation completes without producing any
     diffs (`rotation_had_changes` stays FALSE between two wraps),
     `caught_up` flips TRUE and backfill is skipped from then on.

After the two phases we run an incremental grouper on Neon (rebuilds
gallery_group_members for any gid whose base_title acquired new siblings).
"""

import logging
import os
import signal
import time
from pathlib import Path

import boto3
import psycopg2
import psycopg2.extras
from botocore.exceptions import ClientError

# Auto-encode Python dict as JSONB on bind (tags column round-trips Pi -> Neon).
psycopg2.extensions.register_adapter(dict, psycopg2.extras.Json)

# ─── Config ─────────────────────────────────────────────────────────────────

PI_DSN          = os.environ["PI_DSN"]
NEON_DSN        = os.environ["NEON_DSN"]
R2_ENDPOINT     = os.environ["R2_ENDPOINT"]
R2_BUCKET       = os.environ["R2_BUCKET"]
R2_KEY_ID       = os.environ["R2_ACCESS_KEY_ID"]
R2_SECRET       = os.environ["R2_SECRET_ACCESS_KEY"]
THUMB_DIR       = Path(os.environ.get("THUMB_DIR", "/data/thumbs"))
CADENCE_SEC     = int(os.environ.get("SYNC_CADENCE_SEC", "300"))
CHUNK_ROT       = int(os.environ.get("SYNC_CHUNK_ROT", "5000"))
OUTBOX_BATCH    = int(os.environ.get("SYNC_OUTBOX_BATCH", "500"))
ONESHOT         = os.environ.get("SYNC_ONESHOT") == "1"

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("pi-sync")

# Columns synced Pi -> Neon. row_updated_at is Neon-only, computed server-side.
# Includes 006_detail_extras fields (file_size, is_expunged, etc.) so that
# detail-page data captured by the scraper propagates to the public database.
COLS = [
    "gid", "token", "category", "title", "title_jpn", "uploader",
    "posted_at", "language", "pages", "rating", "fav_count", "thumb",
    "comment_count", "tags", "last_synced_at", "is_active", "base_title",
    "file_size", "file_size_bytes", "rating_count", "visible",
    "parent_gid", "torrent_count", "is_expunged",
]
COL_LIST = ", ".join(COLS)
PH       = ", ".join(["%s"] * len(COLS))
SET_LIST = ", ".join(f"{c} = EXCLUDED.{c}" for c in COLS if c != "gid")

UPSERT_SQL = f"""
INSERT INTO eh_galleries ({COL_LIST}, row_updated_at)
VALUES ({PH}, NOW())
ON CONFLICT (gid) DO UPDATE SET
  {SET_LIST}, row_updated_at = NOW()
"""

GROUPER_INC_SQL = """
WITH new_galleries AS (
  SELECT gid, base_title FROM eh_galleries
  WHERE base_title IS NOT NULL AND base_title <> ''
    AND gid NOT IN (SELECT gid FROM gallery_group_members)
),
matching AS (
  SELECT g.gid, g.base_title FROM eh_galleries g
  WHERE g.base_title IS NOT NULL AND g.base_title <> ''
    AND g.base_title IN (SELECT base_title FROM new_galleries)
),
multi AS (
  SELECT base_title FROM matching GROUP BY base_title HAVING COUNT(*) > 1
),
grouped AS (
  SELECT MIN(m.gid) OVER (PARTITION BY m.base_title) AS group_id, m.gid
  FROM matching m JOIN multi mu USING (base_title)
)
INSERT INTO gallery_group_members (group_id, gid)
SELECT group_id, gid FROM grouped
ON CONFLICT (gid) DO UPDATE SET group_id = EXCLUDED.group_id
"""

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


# ─── Pi helpers ─────────────────────────────────────────────────────────────

def pi_load_state(pi_conn):
    with pi_conn.cursor() as cur:
        cur.execute(
            "SELECT cursor_gid, caught_up, rotation_had_changes FROM sync_state WHERE id = 1"
        )
        row = cur.fetchone()
    return row  # (cursor_gid, caught_up, rotation_had_changes)


def pi_save_state(pi_conn, cursor_gid, caught_up, rotation_had_changes):
    with pi_conn.cursor() as cur:
        cur.execute(
            "UPDATE sync_state SET cursor_gid = %s, caught_up = %s, "
            "rotation_had_changes = %s, updated_at = NOW() WHERE id = 1",
            (cursor_gid, caught_up, rotation_had_changes),
        )
    pi_conn.commit()


def pi_fetch_rot_summary(pi_conn, gid_lt, limit):
    with pi_conn.cursor() as cur:
        if gid_lt is None:
            cur.execute(
                "SELECT gid, fav_count, is_active, file_size FROM eh_galleries "
                "ORDER BY gid DESC LIMIT %s",
                (limit,),
            )
        else:
            cur.execute(
                "SELECT gid, fav_count, is_active, file_size FROM eh_galleries "
                "WHERE gid < %s ORDER BY gid DESC LIMIT %s",
                (gid_lt, limit),
            )
        return cur.fetchall()


def pi_fetch_full(pi_conn, gids):
    if not gids:
        return []
    with pi_conn.cursor() as cur:
        cur.execute(
            f"SELECT {COL_LIST} FROM eh_galleries WHERE gid = ANY(%s)",
            (list(gids),),
        )
        return cur.fetchall()


def pi_outbox_peek(pi_conn, limit):
    with pi_conn.cursor() as cur:
        cur.execute(
            "SELECT gid, enqueued_at FROM sync_outbox "
            "ORDER BY enqueued_at LIMIT %s",
            (limit,),
        )
        return cur.fetchall()


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


# ─── Neon helpers ───────────────────────────────────────────────────────────

def neon_fetch_summary(neon_conn, gids):
    if not gids:
        return {}
    with neon_conn.cursor() as cur:
        cur.execute(
            "SELECT gid, fav_count, is_active, file_size FROM eh_galleries "
            "WHERE gid = ANY(%s)",
            (list(gids),),
        )
        return {r[0]: (r[1], r[2], r[3]) for r in cur.fetchall()}


def neon_upsert_one(neon_conn, row):
    with neon_conn.cursor() as cur:
        cur.execute(UPSERT_SQL, row)
    neon_conn.commit()


def neon_upsert_many(neon_conn, rows):
    if not rows:
        return
    with neon_conn.cursor() as cur:
        psycopg2.extras.execute_batch(cur, UPSERT_SQL, rows, page_size=200)
    neon_conn.commit()


def neon_run_grouper(neon_conn):
    with neon_conn.cursor() as cur:
        cur.execute(GROUPER_INC_SQL)
        affected = cur.rowcount
    neon_conn.commit()
    return affected


# ─── Phase 1: outbox drain ──────────────────────────────────────────────────

def drain_outbox(pi_conn, neon_conn):
    """Returns (pushed, skip_no_file, skip_r2_err, kept_due_to_race)."""
    rows = pi_outbox_peek(pi_conn, OUTBOX_BATCH)
    if not rows:
        return (0, 0, 0, 0)

    pushed = no_file = r2_err = kept = 0
    gids = [r[0] for r in rows]
    full_map = {r[0]: r for r in pi_fetch_full(pi_conn, gids)}

    # Batch-check which gids already exist on Neon — only new gids need
    # R2 thumb upload. Existing gids just need a Neon UPSERT (detail field
    # updates, fav_count changes, etc. don't change the thumbnail).
    existing_gids = set(neon_fetch_summary(neon_conn, gids).keys())

    # Separate into new (need R2) and existing (skip R2)
    new_rows = []
    existing_rows = []
    for gid, enq in rows:
        full = full_map.get(gid)
        if full is None:
            # Pi row deleted between peek and full fetch — drop the outbox entry.
            pi_outbox_delete_if_unchanged(pi_conn, gid, enq)
            continue
        if gid in existing_gids:
            existing_rows.append((gid, enq, full))
        else:
            new_rows.append((gid, enq, full))

    # New gids: R2 PUT + Neon UPSERT (need thumbnail on R2)
    for gid, enq, full in new_rows:
        result = r2_put_thumb(gid)
        if result == "no_file":
            no_file += 1
            continue
        if result == "error":
            r2_err += 1
            continue
        try:
            neon_upsert_one(neon_conn, full)
        except psycopg2.OperationalError as e:
            # Connection dropped (Neon resumed/closed). Abort cycle so the
            # outer loop reconnects fresh next iteration.
            log.warning("outbox gid=%d UPSERT failed (connection dead): %s", gid, e)
            raise
        except Exception as e:
            log.warning("outbox gid=%d UPSERT failed: %s", gid, e)
            try:
                neon_conn.rollback()
            except Exception:
                pass
            continue
        if pi_outbox_delete_if_unchanged(pi_conn, gid, enq):
            pushed += 1
        else:
            kept += 1

    # Existing gids: batch UPSERT only, skip R2 (thumb already uploaded)
    if existing_rows:
        try:
            neon_upsert_many(neon_conn, [r[2] for r in existing_rows])
        except psycopg2.OperationalError as e:
            log.warning("outbox batch UPSERT failed (connection dead): %s", e)
            raise
        except Exception as e:
            log.warning("outbox batch UPSERT failed: %s", e)
            try:
                neon_conn.rollback()
            except Exception:
                pass
            existing_rows = []  # skip deletion below
        for gid, enq, _ in existing_rows:
            if pi_outbox_delete_if_unchanged(pi_conn, gid, enq):
                pushed += 1
            else:
                kept += 1

    return (pushed, no_file, r2_err, kept)


# ─── Phase 2: rotating backfill ─────────────────────────────────────────────

def backfill_chunk(pi_conn, neon_conn, prev_cursor):
    """
    Returns (new_count, changed_count, next_cursor, wrap_happened).

    wrap_happened means this chunk completed a rotation (cursor moved from
    a real gid back to NULL); used by the caller to evaluate catch-up.
    """
    rows = pi_fetch_rot_summary(pi_conn, gid_lt=prev_cursor, limit=CHUNK_ROT)
    if not rows:
        # Either table is empty, or cursor sits below the bottom — wrap.
        return (0, 0, None, prev_cursor is not None)

    pi_subset = rows
    pi_gids   = [r[0] for r in pi_subset]
    neon_map  = neon_fetch_summary(neon_conn, pi_gids)

    new_gids = [r[0] for r in pi_subset if r[0] not in neon_map]
    changed_gids = [
        r[0] for r in pi_subset
        if r[0] in neon_map and (
            # fav_count or is_active changed
            (r[1], r[2]) != (neon_map[r[0]][0], neon_map[r[0]][1])
            # Pi has detail (file_size NOT NULL) but Neon doesn't — backfill needed
            or (r[3] is not None and neon_map[r[0]][2] is None)
        )
    ]

    new_full = {r[0]: r for r in pi_fetch_full(pi_conn, new_gids)}
    for gid in new_gids:
        full = new_full.get(gid)
        if full is None:
            continue
        result = r2_put_thumb(gid)
        if result != "ok":
            continue
        try:
            neon_upsert_one(neon_conn, full)
        except Exception as e:
            log.warning("backfill new gid=%d UPSERT failed: %s", gid, e)
            neon_conn.rollback()

    if changed_gids:
        changed_full = pi_fetch_full(pi_conn, changed_gids)
        try:
            neon_upsert_many(neon_conn, changed_full)
        except Exception as e:
            log.warning("backfill changed UPSERT batch failed: %s", e)
            neon_conn.rollback()

    # Cursor advance
    if len(rows) == CHUNK_ROT:
        next_cursor = min(r[0] for r in rows)
        wrap = False
    else:
        next_cursor = None
        wrap = True

    return (len(new_gids), len(changed_gids), next_cursor, wrap)


# ─── Cycle ──────────────────────────────────────────────────────────────────

def run_cycle(pi_conn, neon_conn):
    state = pi_load_state(pi_conn)
    if state is None:
        log.error("sync_state row missing; migration 009/011 applied?")
        return
    prev_cursor, caught_up, rotation_had_changes = state

    # Phase 1: outbox
    obx_pushed, obx_no_file, obx_r2_err, obx_kept = drain_outbox(pi_conn, neon_conn)

    # Phase 2: backfill (only while not caught up)
    bf_new = bf_changed = 0
    next_cursor = prev_cursor
    wrap = False
    if not caught_up:
        bf_new, bf_changed, next_cursor, wrap = backfill_chunk(
            pi_conn, neon_conn, prev_cursor
        )

    # Catch-up bookkeeping
    new_caught_up = caught_up
    new_rotation_had_changes = rotation_had_changes
    if not caught_up:
        if bf_new > 0 or bf_changed > 0:
            new_rotation_had_changes = True
        if wrap:
            if not new_rotation_had_changes:
                new_caught_up = True
                log.info("backfill caught up — outbox-only from now on")
            # Reset for the next rotation
            new_rotation_had_changes = False

    # Phase 3: grouper (always — outbox might have produced new rows)
    try:
        group_affected = neon_run_grouper(neon_conn)
    except Exception as e:
        log.warning("grouper failed: %s", e)
        neon_conn.rollback()
        group_affected = -1

    # Persist state (after work)
    pi_save_state(pi_conn, next_cursor, new_caught_up, new_rotation_had_changes)

    log.info(
        "cycle: outbox(pushed=%d no_file=%d r2_err=%d kept=%d) "
        "backfill(new=%d changed=%d wrap=%s) grouper=%d caught_up=%s cursor=%s",
        obx_pushed, obx_no_file, obx_r2_err, obx_kept,
        bf_new, bf_changed, wrap,
        group_affected, new_caught_up, next_cursor,
    )


# ─── Main loop ──────────────────────────────────────────────────────────────

def main():
    log.info(
        "pi-sync starting: cadence=%ds rot=%d outbox_batch=%d thumbs=%s",
        CADENCE_SEC, CHUNK_ROT, OUTBOX_BATCH, THUMB_DIR,
    )
    while not _stopping:
        t0 = time.monotonic()
        pi = neon = None
        try:
            pi   = psycopg2.connect(PI_DSN)
            # TCP keepalives so Neon-side drops surface quickly instead of
            # appearing alive until the first write.
            neon = psycopg2.connect(
                NEON_DSN,
                keepalives=1, keepalives_idle=30,
                keepalives_interval=10, keepalives_count=3,
            )
            run_cycle(pi, neon)
        except Exception as e:
            log.exception("cycle failed: %s", e)
        finally:
            for c in (pi, neon):
                if c is not None:
                    try:
                        c.close()
                    except Exception:
                        pass
        elapsed = time.monotonic() - t0
        if _stopping or ONESHOT:
            log.info("cycle done in %.1fs%s", elapsed, " (oneshot, exiting)" if ONESHOT else "")
            break
        log.info("cycle done in %.1fs, sleeping %ds", elapsed, CADENCE_SEC)
        _interruptible_sleep(CADENCE_SEC)
    log.info("pi-sync stopped")


if __name__ == "__main__":
    main()
