import json
import math
import select as select_mod
import time
from typing import Any, Dict

import psycopg2
import psycopg2.extensions
from fastapi import APIRouter, Depends, Header, HTTPException, Query, status
from fastapi.responses import StreamingResponse
from pydantic import BaseModel

from db import DATABASE_URL, get_cursor, get_db
from models import (
    CloudSyncStatus,
    EmbeddingsStatus,
    FAVORITES_CATEGORY,
    MIXED_CATEGORY,
    REFRESH_CATEGORY,
    SimilarityDistribution,
    SyncTask,
    SyncTaskCreate,
    SyncTaskUpdate,
    ThumbQueueStats,
    VALID_CATEGORIES,
)

router = APIRouter(prefix="/v1/admin", tags=["admin"])

DEFAULT_FULL_CONFIG = {
    "inline_set": "dm_e",
    "start_gid": None,
}

DEFAULT_INCREMENTAL_CONFIG = {
    "inline_set": "dm_e",
    "categories": ["Doujinshi", "Manga", "Cosplay"],
    "scan_window": 10000,
    "rating_diff_threshold": 0.5,
}

DEFAULT_FAVORITES_CONFIG = {
    "run_interval_hours": 6,
}

DEFAULT_REFRESH_CONFIG = {
    "batch_size": 25,
    "min_fav": 200,
}

TASK_DEF_BASE_SELECT = """
    SELECT d.*,
           {job_columns}
    FROM sync_task_defs d
    {job_joins}
"""

TASK_DEF_RIVER_COLUMNS = """
           cj.state::text AS current_job_state,
           cj.kind AS current_job_kind,
           cj.attempt AS current_job_attempt,
           cj.max_attempts AS current_job_max_attempts,
           cj.scheduled_at AS current_job_scheduled_at,
           cj.attempted_at AS current_job_attempted_at,
           cj.finalized_at AS current_job_finalized_at,
           lj.state::text AS latest_job_state,
           lj.kind AS latest_job_kind,
           lj.attempt AS latest_job_attempt,
           lj.max_attempts AS latest_job_max_attempts,
           lj.scheduled_at AS latest_job_scheduled_at,
           lj.attempted_at AS latest_job_attempted_at,
           lj.finalized_at AS latest_job_finalized_at
"""

TASK_DEF_NULL_JOB_COLUMNS = """
           NULL::text AS current_job_state,
           NULL::text AS current_job_kind,
           NULL::integer AS current_job_attempt,
           NULL::integer AS current_job_max_attempts,
           NULL::timestamptz AS current_job_scheduled_at,
           NULL::timestamptz AS current_job_attempted_at,
           NULL::timestamptz AS current_job_finalized_at,
           NULL::text AS latest_job_state,
           NULL::text AS latest_job_kind,
           NULL::integer AS latest_job_attempt,
           NULL::integer AS latest_job_max_attempts,
           NULL::timestamptz AS latest_job_scheduled_at,
           NULL::timestamptz AS latest_job_attempted_at,
           NULL::timestamptz AS latest_job_finalized_at
"""

TASK_DEF_RIVER_JOINS = """
    LEFT JOIN river_job cj ON cj.id = d.current_job_id
    LEFT JOIN river_job lj ON lj.id = d.last_job_id
"""


def _task_def_select(db) -> str:
    db.execute("SELECT to_regclass('public.river_job')")
    has_river_jobs = db.fetchone()[0] is not None
    return TASK_DEF_BASE_SELECT.format(
        job_columns=TASK_DEF_RIVER_COLUMNS if has_river_jobs else TASK_DEF_NULL_JOB_COLUMNS,
        job_joins=TASK_DEF_RIVER_JOINS if has_river_jobs else "",
    )


def _sse(data: Dict[str, Any], event: str = "message", event_id: int | None = None) -> str:
    lines = []
    if event_id is not None:
        lines.append(f"id: {event_id}")
    lines.append(f"event: {event}")
    lines.append(f"data: {json.dumps(data, default=str, ensure_ascii=False)}")
    return "\n".join(lines) + "\n\n"


def _init_state(task_type: str, cfg: Dict[str, Any]) -> Dict[str, Any]:
    if task_type == "full":
        return {
            "next_gid": cfg.get("start_gid"),
            "round": 0,
            "done": False,
            "anchor_gid": None,
            "total_count": None,
        }
    if task_type == "favorites":
        return {"round": 0}
    if task_type == "refresh_detail":
        return {
            "offset": 0,
            "total_done": 0,
            "total_pending": None,
        }
    return {
        "next_gid": None,
        "round": 0,
        "latest_gid": None,
        "scanned_count": 0,
    }


def _normalize_config(task_type: str, config: Dict[str, Any]) -> Dict[str, Any]:
    raw = dict(config or {})
    if task_type == "full":
        merged = dict(DEFAULT_FULL_CONFIG)
        merged["start_gid"] = raw.get("start_gid")
        merged["inline_set"] = "dm_e"  # 始终写死，不允许覆盖
        return merged

    if task_type == "favorites":
        merged = dict(DEFAULT_FAVORITES_CONFIG)
        try:
            merged["run_interval_hours"] = max(1, float(raw.get("run_interval_hours", 6)))
        except (TypeError, ValueError):
            pass
        return merged

    if task_type == "refresh_detail":
        merged = dict(DEFAULT_REFRESH_CONFIG)
        try:
            merged["batch_size"] = max(1, int(raw.get("batch_size", 25)))
        except (TypeError, ValueError):
            pass
        try:
            merged["min_fav"] = max(0, int(raw.get("min_fav", 0)))
        except (TypeError, ValueError):
            pass
        return merged

    # incremental: strict schema, no legacy keys compatibility.
    cats = raw.get("categories")
    if not isinstance(cats, list) or not cats:
        raise HTTPException(status_code=422, detail="incremental config.categories must be a non-empty list")

    normalized: list[str] = []
    seen: set[str] = set()
    for item in cats:
        if not isinstance(item, str):
            raise HTTPException(status_code=422, detail="incremental config.categories must be a list of strings")
        value = item.strip()
        if value not in VALID_CATEGORIES:
            raise HTTPException(
                status_code=422,
                detail=f"invalid category '{value}' in config.categories",
            )
        if value not in seen:
            seen.add(value)
            normalized.append(value)

    merged = dict(DEFAULT_INCREMENTAL_CONFIG)
    merged["categories"] = normalized
    try:
        merged["scan_window"] = int(raw.get("scan_window") or DEFAULT_INCREMENTAL_CONFIG["scan_window"])
    except (TypeError, ValueError) as exc:
        raise HTTPException(status_code=422, detail="incremental config.scan_window must be an integer") from exc
    try:
        merged["rating_diff_threshold"] = float(
            raw.get("rating_diff_threshold") or DEFAULT_INCREMENTAL_CONFIG["rating_diff_threshold"]
        )
    except (TypeError, ValueError) as exc:
        raise HTTPException(status_code=422, detail="incremental config.rating_diff_threshold must be a number") from exc
    merged["inline_set"] = "dm_e"  # 始终写死，不允许覆盖
    return merged


ACTIVE_JOB_STATES = {"available", "pending", "scheduled", "running", "retryable"}

PHASES = ("disabled", "running", "retrying", "queued", "paused", "failed", "idle")


def _derive_phase(item: Dict[str, Any]) -> tuple[str, str]:
    """Single source of truth for a task's display state.

    Rules are evaluated top-down; phase_reason names the rule that fired so
    the derivation itself is never a black box. Raw river fields stay
    available on the model for deep debugging.
    """
    enabled = bool(item.get("enabled"))
    schedule_kind = item.get("schedule_kind")
    requested_action = item.get("requested_action")
    cur_state = item.get("current_job_state")
    cur_attempt = item.get("current_job_attempt")
    cur_max = item.get("current_job_max_attempts")
    latest_state = item.get("latest_job_state")
    last_error = item.get("last_error")
    checkpoint = dict(item.get("checkpoint") or {})
    interval = item.get("schedule_interval_sec")

    # An unconsumed user action is worth surfacing regardless of which rule wins.
    suffix = f" · requested_action={requested_action} pending" if requested_action else ""

    if cur_state == "running":
        return "running", f"river job #{item.get('current_job_id')} running (attempt {cur_attempt}/{cur_max})" + suffix
    if cur_state == "retryable":
        return "retrying", (
            f"river job #{item.get('current_job_id')} failed, retry scheduled "
            f"(attempt {cur_attempt}/{cur_max}, at {item.get('current_job_scheduled_at')})"
        ) + suffix
    if cur_state in {"available", "pending", "scheduled"}:
        run_id = checkpoint.get("run_id")
        chain = f" · slice chain run_id={run_id}" if run_id else ""
        return "queued", f"river job #{item.get('current_job_id')} waiting for worker (state={cur_state}){chain}" + suffix
    if requested_action:
        return "queued", f"requested_action={requested_action} awaiting scheduler pickup (poll ≤5s)"
    if not enabled:
        return "disabled", "definition disabled"
    if latest_state == "discarded":
        detail = f": {last_error}" if last_error else ""
        return "failed", f"last river job #{item.get('last_job_id')} discarded (retries exhausted){detail}"
    if last_error and last_error.startswith("exit: "):
        return "paused", f"last round ended with {last_error[6:]}; next periodic kick starts a new round"
    if schedule_kind == "periodic":
        every = f"{interval}s" if interval else "default interval"
        return "idle", f"waiting next periodic tick (every {every})"
    return "idle", "enabled, no job queued"


def _derive_type(source: str | None, strategy: str | None) -> str:
    # Config normalization still keys off this legacy label; derive it from
    # the canonical source/strategy fields instead of storing duplicates.
    if source == "favorites":
        return "favorites"
    if source == "refresh_detail":
        return "refresh_detail"
    if strategy == "incremental":
        return "incremental"
    return "full"


def _task_def_from_row(db, row) -> SyncTask:
    cols = [d[0] for d in db.description]
    item = dict(zip(cols, row))
    progress = dict(item.get("progress") or {})
    checkpoint = dict(item.get("checkpoint") or {})
    scope = dict(item.get("scope") or {})
    enabled = bool(item.get("enabled"))
    error = item.get("last_error")
    source = item.get("source")
    strategy = item.get("strategy")
    current_job_state = item.get("current_job_state")
    latest_job_state = item.get("latest_job_state")
    schedule_kind = item.get("schedule_kind")
    phase, phase_reason = _derive_phase(item)

    return SyncTask(
        id=item["id"],
        name=item["name"],
        phase=phase,
        phase_reason=phase_reason,
        config=item.get("config") or {},
        progress_pct=float(progress.get("pct") or 0),
        created_at=item.get("created_at"),
        updated_at=item.get("updated_at"),
        last_run_at=item.get("last_run_at"),
        error_message=error,
        enabled=enabled,
        task_kind=item.get("task_kind"),
        source=source,
        strategy=strategy,
        scope=scope,
        checkpoint=checkpoint,
        progress=progress,
        current_job_id=item.get("current_job_id"),
        last_job_id=item.get("last_job_id"),
        current_job_state=current_job_state,
        current_job_kind=item.get("current_job_kind"),
        current_job_attempt=item.get("current_job_attempt"),
        current_job_max_attempts=item.get("current_job_max_attempts"),
        current_job_scheduled_at=item.get("current_job_scheduled_at"),
        current_job_attempted_at=item.get("current_job_attempted_at"),
        current_job_finalized_at=item.get("current_job_finalized_at"),
        latest_job_state=latest_job_state,
        latest_job_kind=item.get("latest_job_kind"),
        latest_job_attempt=item.get("latest_job_attempt"),
        latest_job_max_attempts=item.get("latest_job_max_attempts"),
        latest_job_scheduled_at=item.get("latest_job_scheduled_at"),
        latest_job_attempted_at=item.get("latest_job_attempted_at"),
        latest_job_finalized_at=item.get("latest_job_finalized_at"),
        schedule_kind=schedule_kind,
        schedule_interval_sec=item.get("schedule_interval_sec"),
        next_run_at=item.get("next_run_at"),
        last_finished_at=item.get("last_finished_at"),
        requested_action=item.get("requested_action"),
    )


def _get_task_def_or_404(task_id: int, db) -> SyncTask:
    db.execute(_task_def_select(db) + " WHERE d.id = %s", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return _task_def_from_row(db, row)


def _get_task_or_404(task_id: int, db) -> SyncTask:
    db.execute(_task_def_select(db) + " WHERE d.id = %s", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return _task_def_from_row(db, row)


@router.post("/tasks", response_model=SyncTask, status_code=status.HTTP_201_CREATED)
def create_task(payload: SyncTaskCreate, db=Depends(get_db)):
    if payload.type == "incremental":
        if payload.category != MIXED_CATEGORY:
            raise HTTPException(status_code=422, detail=f"incremental category must be '{MIXED_CATEGORY}'")
        db.execute("SELECT id, name FROM sync_task_defs WHERE source = 'gallery_list' AND strategy = 'incremental' LIMIT 1")
        existing = db.fetchone()
        if existing:
            raise HTTPException(
                status_code=409,
                detail=f"Only one incremental task is allowed (existing id={existing[0]} name={existing[1]})",
            )
    elif payload.type == "favorites":
        if payload.category != FAVORITES_CATEGORY:
            raise HTTPException(status_code=422, detail=f"favorites category must be '{FAVORITES_CATEGORY}'")
        db.execute("SELECT id, name FROM sync_task_defs WHERE source = 'favorites' LIMIT 1")
        existing = db.fetchone()
        if existing:
            raise HTTPException(
                status_code=409,
                detail=f"Only one favorites task is allowed (existing id={existing[0]} name={existing[1]})",
            )
    elif payload.type == "refresh_detail":
        if payload.category != REFRESH_CATEGORY:
            raise HTTPException(status_code=422, detail=f"refresh_detail category must be '{REFRESH_CATEGORY}'")
        db.execute("SELECT id, name FROM sync_task_defs WHERE source = 'refresh_detail' LIMIT 1")
        existing = db.fetchone()
        if existing:
            raise HTTPException(
                status_code=409,
                detail=f"Only one refresh_detail task is allowed (existing id={existing[0]} name={existing[1]})",
            )

    cfg = _normalize_config(payload.type, payload.config)
    state = _init_state(payload.type, cfg)
    progress = {"pct": 0}
    if payload.type == "favorites":
        task_kind = "favorites_sync"
        source = "favorites"
        strategy = "full"
    elif payload.type == "refresh_detail":
        task_kind = "refresh_detail"
        source = "refresh_detail"
        strategy = "refresh"
    elif payload.type == "incremental":
        task_kind = "gallery_sync"
        source = "gallery_list"
        strategy = "incremental"
    else:
        task_kind = "gallery_sync"
        source = "gallery_list"
        strategy = "full"
    if payload.type == "incremental":
        scope = {"categories": cfg.get("categories", [])}
    elif payload.type == "favorites":
        scope = {"target": "user_favorites"}
    elif payload.type == "refresh_detail":
        scope = {"target": "stale_detail_rows"}
    else:
        scope = {"category": payload.category}
    schedule_kind = "periodic" if payload.type in {"favorites", "incremental", "refresh_detail"} else "manual"
    interval_sec = None
    enabled = payload.type in {"favorites", "incremental", "refresh_detail"}
    if payload.type == "favorites":
        interval_sec = int(float(cfg.get("run_interval_hours", 6)) * 3600)
    elif payload.type == "incremental":
        interval_sec = 30
    elif payload.type == "refresh_detail":
        interval_sec = 300

    try:
        db.execute(
            """
            INSERT INTO sync_task_defs (
                name, task_kind, source, strategy, scope,
                enabled, config, checkpoint, progress, schedule_kind, schedule_interval_sec
            )
            VALUES (%s, %s, %s, %s, %s::jsonb, %s, %s::jsonb, %s::jsonb, %s::jsonb, %s, %s)
            RETURNING *
            """,
            (
                payload.name, task_kind, source, strategy, json.dumps(scope),
                enabled, json.dumps(cfg), json.dumps(state),
                json.dumps(progress), schedule_kind, interval_sec,
            ),
        )
    except Exception as exc:
        msg = str(exc).lower()
        if "duplicate key value" in msg and "sync_task_defs_name_key" in msg:
            raise HTTPException(status_code=409, detail="Task name already exists") from exc
        raise

    row = db.fetchone()
    return _task_def_from_row(db, row)


@router.get("/tasks", response_model=list[SyncTask])
def list_tasks(db=Depends(get_db)):
    db.execute(_task_def_select(db) + " ORDER BY d.id ASC")
    rows = db.fetchall()
    return [_task_def_from_row(db, row) for row in rows]


@router.get("/tasks/{task_id}", response_model=SyncTask)
def get_task(task_id: int, db=Depends(get_db)):
    return _get_task_or_404(task_id, db)


@router.get("/events")
def admin_events(
    after_id: int = Query(0, ge=0),
    last_event_id: str | None = Header(default=None, alias="Last-Event-ID"),
):
    try:
        resumed_after = int(last_event_id) if last_event_id else 0
    except (TypeError, ValueError):
        resumed_after = 0
    start_after = max(after_id, resumed_after)

    def stream():
        last_id = start_after
        # Dedicated LISTEN connection: new rows wake the stream instantly via
        # the task_events trigger; the 5s select timeout keeps the old ping
        # cadence and doubles as fallback if the trigger is missing.
        listen_conn = psycopg2.connect(DATABASE_URL)
        try:
            listen_conn.set_isolation_level(psycopg2.extensions.ISOLATION_LEVEL_AUTOCOMMIT)
            with listen_conn.cursor() as lcur:
                lcur.execute("LISTEN task_events;")
            while True:
                emitted = False
                with get_cursor() as cur:
                    cur.execute(
                        """
                        SELECT id, task_id, job_id, event_type, message, payload, created_at
                        FROM sync_task_events
                        WHERE id > %s
                        ORDER BY id ASC
                        LIMIT 100
                        """,
                        (last_id,),
                    )
                    for row in cur.fetchall():
                        event_id, task_id, job_id, event_type, message, payload, created_at = row
                        last_id = event_id
                        emitted = True
                        yield _sse(
                            {
                                "id": event_id,
                                "task_id": task_id,
                                "job_id": job_id,
                                "type": event_type,
                                "message": message,
                                "payload": payload or {},
                                "created_at": created_at,
                            },
                            event="admin.task",
                            event_id=event_id,
                        )
                if not emitted:
                    yield _sse({"ts": time.time()}, event="ping")
                if select_mod.select([listen_conn], [], [], 5)[0]:
                    listen_conn.poll()
                    listen_conn.notifies.clear()
        finally:
            listen_conn.close()

    return StreamingResponse(stream(), media_type="text/event-stream")


@router.get("/tasks/{task_id}/events")
def task_events(
    task_id: int,
    limit: int = Query(50, ge=1, le=200),
    before_id: int | None = Query(None, ge=1),
    db=Depends(get_db),
):
    """Recent timeline events for one task, newest first. before_id pages
    backwards through history."""
    _get_task_or_404(task_id, db)
    params: list[Any] = [task_id]
    where = "task_id = %s"
    if before_id is not None:
        where += " AND id < %s"
        params.append(before_id)
    params.append(limit)
    db.execute(
        f"""
        SELECT id, job_id, event_type, message, payload, created_at
        FROM sync_task_events
        WHERE {where}
        ORDER BY id DESC
        LIMIT %s
        """,
        tuple(params),
    )
    return [
        {
            "id": row[0],
            "job_id": row[1],
            "type": row[2],
            "message": row[3],
            "payload": row[4] or {},
            "created_at": row[5],
        }
        for row in db.fetchall()
    ]


@router.patch("/tasks/{task_id}", response_model=SyncTask)
def patch_task(task_id: int, payload: SyncTaskUpdate, db=Depends(get_db)):
    db.execute("SELECT id, name, source, strategy, config FROM sync_task_defs WHERE id = %s", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")

    _, curr_name, source, strategy, curr_config = row
    task_type = _derive_type(source, strategy)
    name = payload.name if payload.name is not None else curr_name
    config = dict(curr_config or {})
    if payload.config:
        config.update(payload.config)
    config = _normalize_config(task_type, config)

    try:
        db.execute(
            """
            UPDATE sync_task_defs
            SET name = %s, config = %s::jsonb, updated_at = NOW()
            WHERE id = %s
            RETURNING *
            """,
            (name, json.dumps(config), task_id),
        )
    except Exception as exc:
        msg = str(exc).lower()
        if "duplicate key value" in msg and "sync_task_defs_name_key" in msg:
            raise HTTPException(status_code=409, detail="Task name already exists") from exc
        raise

    db.execute(_task_def_select(db) + " WHERE d.id = %s", (task_id,))
    return _task_def_from_row(db, db.fetchone())


@router.post("/tasks/{task_id}/start", response_model=SyncTask)
def start_task(task_id: int, db=Depends(get_db)):
    task = _get_task_or_404(task_id, db)
    if task.current_job_state in {"available", "pending", "scheduled", "running", "retryable"}:
        return task

    db.execute(
        """
        UPDATE sync_task_defs
        SET enabled = TRUE,
            requested_action = 'start',
            requested_at = NOW(),
            updated_at = NOW()
        WHERE id = %s
        RETURNING *
        """,
        (task_id,),
    )
    row = db.fetchone()
    return _task_def_from_row(db, row)


@router.post("/tasks/{task_id}/stop", response_model=SyncTask)
def stop_task(task_id: int, db=Depends(get_db)):
    task = _get_task_or_404(task_id, db)
    if not task.enabled and task.current_job_state not in {"available", "pending", "scheduled", "running", "retryable"}:
        return task

    db.execute(
        """
        UPDATE sync_task_defs
        SET enabled = FALSE,
            requested_action = 'stop',
            requested_at = NOW(),
            updated_at = NOW()
        WHERE id = %s
        RETURNING *
        """,
        (task_id,),
    )
    row = db.fetchone()
    return _task_def_from_row(db, row)


@router.post("/tasks/{task_id}/retry", response_model=SyncTask)
def retry_task(task_id: int, db=Depends(get_db)):
    task = _get_task_or_404(task_id, db)
    if task.current_job_state in {"available", "pending", "scheduled", "running", "retryable"}:
        raise HTTPException(status_code=409, detail="Task is already active")
    db.execute(
        """
        UPDATE sync_task_defs
        SET enabled = TRUE,
            requested_action = 'retry',
            requested_at = NOW(),
            updated_at = NOW()
        WHERE id = %s
        RETURNING *
        """,
        (task_id,),
    )
    return _task_def_from_row(db, db.fetchone())


@router.delete("/tasks/{task_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_task(task_id: int, confirm: bool = Query(False), db=Depends(get_db)):
    if not confirm:
        raise HTTPException(status_code=400, detail="Delete requires confirm=true")

    task = _get_task_or_404(task_id, db)
    if task.current_job_state in {"available", "pending", "scheduled", "running", "retryable"} or task.enabled:
        raise HTTPException(status_code=409, detail="Stop task before deleting")

    db.execute("DELETE FROM sync_task_defs WHERE id = %s RETURNING id", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return None


@router.get("/thumb-queue/stats", response_model=ThumbQueueStats)
def thumb_queue_stats(db=Depends(get_db)):
    db.execute(
        """
        SELECT
            COALESCE(SUM(CASE WHEN status = 'pending' AND (next_retry_at IS NULL OR next_retry_at <= NOW()) THEN 1 ELSE 0 END), 0) AS pending,
            COALESCE(SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END), 0) AS processing,
            COALESCE(SUM(CASE WHEN status = 'done' THEN 1 ELSE 0 END), 0) AS done,
            COALESCE(SUM(CASE WHEN status = 'pending' AND next_retry_at > NOW() THEN 1 ELSE 0 END), 0) AS waiting
        FROM thumb_queue
        """
    )
    row = db.fetchone()
    return ThumbQueueStats(
        pending=row[0],
        processing=row[1],
        done=row[2],
        waiting=row[3],
    )


# ── Pi -> R2 thumbnail sync status ─────────────────────────────────────────

HEARTBEAT_STALE_AFTER_SEC = 180


@router.get("/cloud-sync", response_model=CloudSyncStatus)
def cloud_sync_status(
    limit: int = Query(20, ge=1, le=100),
    db=Depends(get_db),
):
    db.execute(
        """
        SELECT
            NOW(),
            phase,
            worker_started_at,
            heartbeat_at,
            CASE
                WHEN heartbeat_at IS NULL THEN NULL
                ELSE EXTRACT(EPOCH FROM (NOW() - heartbeat_at))::double precision
            END,
            heartbeat_at IS NOT NULL
                AND NOW() - heartbeat_at <= (%s * INTERVAL '1 second'),
            cycle_started_at,
            last_success_at,
            next_run_at,
            current_run_id,
            last_run_id,
            cadence_sec,
            consecutive_failures,
            last_error_kind,
            last_error_message
        FROM cloud_sync_runtime
        WHERE id = 1
        """,
        (HEARTBEAT_STALE_AFTER_SEC,),
    )
    runtime_row = db.fetchone()
    observed_at = runtime_row[0] if runtime_row else None
    runtime = None
    if runtime_row:
        runtime = {
            "phase": runtime_row[1],
            "worker_started_at": runtime_row[2],
            "heartbeat_at": runtime_row[3],
            "heartbeat_age_sec": runtime_row[4],
            "heartbeat_fresh": runtime_row[5],
            "cycle_started_at": runtime_row[6],
            "last_success_at": runtime_row[7],
            "next_run_at": runtime_row[8],
            "current_run_id": runtime_row[9],
            "last_run_id": runtime_row[10],
            "cadence_sec": runtime_row[11],
            "consecutive_failures": runtime_row[12],
            "last_error_kind": runtime_row[13],
            "last_error_message": runtime_row[14],
        }
    else:
        db.execute("SELECT NOW()")
        observed_at = db.fetchone()[0]

    db.execute(
        """
        SELECT
            id,
            trigger,
            cadence_sec,
            status,
            started_at,
            finished_at,
            duration_ms,
            backlog_before,
            backlog_after,
            oldest_pending_before,
            oldest_pending_after,
            selected_count,
            pushed_count,
            no_file_count,
            r2_error_count,
            kept_count,
            group_affected,
            error_kind,
            error_message
        FROM cloud_sync_runs
        ORDER BY started_at DESC
        LIMIT %s
        """,
        (limit,),
    )
    runs = [
        {
            "id": row[0],
            "trigger": row[1],
            "cadence_sec": row[2],
            "status": row[3],
            "started_at": row[4],
            "finished_at": row[5],
            "duration_ms": row[6],
            "backlog_before": row[7],
            "backlog_after": row[8],
            "oldest_pending_before": row[9],
            "oldest_pending_after": row[10],
            "selected_count": row[11],
            "pushed_count": row[12],
            "no_file_count": row[13],
            "r2_error_count": row[14],
            "kept_count": row[15],
            "group_affected": row[16],
            "error_kind": row[17],
            "error_message": row[18],
        }
        for row in db.fetchall()
    ]
    return CloudSyncStatus(
        observed_at=observed_at,
        heartbeat_stale_after_sec=HEARTBEAT_STALE_AFTER_SEC,
        runtime=runtime,
        runs=runs,
    )


@router.get("/cloud-sync/events")
def cloud_sync_events():
    def stream():
        listen_conn = psycopg2.connect(DATABASE_URL)
        try:
            listen_conn.set_isolation_level(psycopg2.extensions.ISOLATION_LEVEL_AUTOCOMMIT)
            with listen_conn.cursor() as lcur:
                lcur.execute("LISTEN cloud_sync_runtime;")
            yield _sse({"ts": time.time()}, event="cloud.sync.ready")
            while True:
                if select_mod.select([listen_conn], [], [], 25)[0]:
                    listen_conn.poll()
                    notifications = list(listen_conn.notifies)
                    listen_conn.notifies.clear()
                    for notification in notifications:
                        try:
                            payload = json.loads(notification.payload)
                        except (TypeError, ValueError):
                            payload = {"updated": True}
                        yield _sse(payload, event="cloud.sync")
                else:
                    yield _sse({"ts": time.time()}, event="ping")
        finally:
            listen_conn.close()

    return StreamingResponse(
        stream(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        },
    )


# ── Similarity Distribution & Threshold (cosine recommendation) ───────────────

DEFAULT_SIMILARITY_THRESHOLD = 0.3


def get_similarity_threshold(db) -> float:
    """Read the persisted similarity threshold from app_settings."""
    db.execute("SELECT value FROM app_settings WHERE key = 'similarity_threshold'")
    row = db.fetchone()
    if not row:
        return DEFAULT_SIMILARITY_THRESHOLD
    try:
        return float(row[0])
    except (TypeError, ValueError):
        return DEFAULT_SIMILARITY_THRESHOLD


@router.get("/recommended/distribution", response_model=SimilarityDistribution)
def recommended_distribution(buckets: int = Query(40, ge=10, le=200), db=Depends(get_db)):
    threshold = get_similarity_threshold(db)

    # Compute similarity for every gallery whose embedding exists, against the
    # single user_profile vector. Materialize once in a CTE to drive both the
    # histogram and the count-above-threshold.
    # similarity is precomputed in recommended_cache by the embeddings worker.
    # Reading is now an indexed column scan; no per-request cosine compute.
    db.execute(
        """
        SELECT COUNT(*), COALESCE(MIN(similarity), 0)::float, COALESCE(MAX(similarity), 1)::float
        FROM recommended_cache
        WHERE similarity IS NOT NULL
        """,
    )
    total, lo, hi = db.fetchone()
    if not total or total == 0:
        return SimilarityDistribution(buckets=[], total=0, threshold=threshold, count_above=0)

    lo = max(0.0, min(1.0, lo))
    hi = max(lo, min(1.0, hi))
    bucket_width = (hi - lo) / buckets if hi > lo else 1.0

    db.execute(
        """
        SELECT width_bucket(similarity, %(lo)s, %(hi_adj)s, %(n)s) AS bucket, COUNT(*) AS cnt
        FROM recommended_cache
        WHERE similarity IS NOT NULL
        GROUP BY bucket
        ORDER BY bucket
        """,
        {"lo": lo, "hi_adj": hi + 1e-6, "n": buckets},
    )
    bucket_map = {r[0]: r[1] for r in db.fetchall()}
    result = []
    for i in range(1, buckets + 1):
        b_lo = lo + (i - 1) * bucket_width
        b_hi = lo + i * bucket_width
        result.append({"min": round(b_lo, 4), "max": round(b_hi, 4), "count": bucket_map.get(i, 0)})

    db.execute(
        "SELECT COUNT(*) FROM recommended_cache WHERE similarity >= %s",
        (threshold,),
    )
    count_above = db.fetchone()[0]

    return SimilarityDistribution(buckets=result, total=total, threshold=threshold, count_above=count_above)


class ThresholdUpdate(BaseModel):
    threshold: float


@router.put("/recommended/threshold")
def update_threshold(payload: ThresholdUpdate, db=Depends(get_db)):
    if payload.threshold < 0 or payload.threshold > 1:
        raise HTTPException(status_code=422, detail="Threshold must be in [0, 1]")
    db.execute(
        """
        INSERT INTO app_settings (key, value) VALUES ('similarity_threshold', %s::jsonb)
        ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
        """,
        (json.dumps(payload.threshold),),
    )
    return {"threshold": payload.threshold}


@router.get("/recommended/embeddings-status", response_model=EmbeddingsStatus)
def embeddings_status(db=Depends(get_db)):
    db.execute(
        """
        SELECT
            (SELECT COUNT(*) FROM tag_vocabulary WHERE is_active = TRUE),
            (SELECT dim_count FROM tag_vocabulary_meta WHERE id = 1),
            (SELECT COUNT(*) FROM eh_galleries WHERE is_active = TRUE),
            (SELECT COUNT(*) FROM recommended_cache rc JOIN eh_galleries g ON g.gid = rc.gid
              WHERE g.is_active = TRUE AND rc.tag_embedding IS NOT NULL),
            (SELECT COUNT(*) FROM recommended_cache rc JOIN eh_galleries g ON g.gid = rc.gid
              WHERE g.is_active = TRUE AND rc.tag_embedding IS NULL),
            (SELECT liked_count FROM user_profile WHERE id = 1),
            (SELECT embedding IS NOT NULL FROM user_profile WHERE id = 1)
        """
    )
    vocab_size, dim_count, total_g, embedded, pending, liked, ready = db.fetchone()
    return EmbeddingsStatus(
        vocab_size=vocab_size or 0,
        dim_count=dim_count or 0,
        total_galleries=total_g or 0,
        embedded_count=embedded or 0,
        pending_count=pending or 0,
        profile_liked_count=liked or 0,
        profile_ready=bool(ready),
    )
