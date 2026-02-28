import json
from typing import Any, Dict

from fastapi import APIRouter, Depends, HTTPException, Query, status

from db import get_db
from models import (
    MIXED_CATEGORY,
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


def _init_state(task_type: str, cfg: Dict[str, Any]) -> Dict[str, Any]:
    if task_type == "full":
        return {
            "next_gid": cfg.get("start_gid"),
            "round": 0,
            "done": False,
            "anchor_gid": None,
            "total_count": None,
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


def _task_from_row(db, row) -> SyncTask:
    cols = [d[0] for d in db.description]
    item = dict(zip(cols, row))
    return SyncTask(**item)


def _get_task_or_404(task_id: int, db) -> SyncTask:
    db.execute("SELECT * FROM sync_tasks WHERE id = %s", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return _task_from_row(db, row)


def _is_transitioning(task: SyncTask) -> bool:
    return (
        (task.status == "stopped" and task.desired_status == "running")
        or (task.status == "running" and task.desired_status == "stopped")
    )


@router.post("/tasks", response_model=SyncTask, status_code=status.HTTP_201_CREATED)
def create_task(payload: SyncTaskCreate, db=Depends(get_db)):
    if payload.type == "incremental":
        if payload.category != MIXED_CATEGORY:
            raise HTTPException(status_code=422, detail=f"incremental category must be '{MIXED_CATEGORY}'")
        db.execute("SELECT id, name FROM sync_tasks WHERE type = 'incremental' LIMIT 1")
        existing = db.fetchone()
        if existing:
            raise HTTPException(
                status_code=409,
                detail=f"Only one incremental task is allowed (existing id={existing[0]} name={existing[1]})",
            )

    cfg = _normalize_config(payload.type, payload.config)
    state = _init_state(payload.type, cfg)

    try:
        db.execute(
            """
            INSERT INTO sync_tasks (name, type, category, config, state, status, desired_status, progress_pct)
            VALUES (%s, %s, %s, %s::jsonb, %s::jsonb, 'stopped', 'stopped', 0)
            RETURNING *
            """,
            (payload.name, payload.type, payload.category, json.dumps(cfg), json.dumps(state)),
        )
    except Exception as exc:
        msg = str(exc).lower()
        if "duplicate key value" in msg and "sync_tasks_name_key" in msg:
            raise HTTPException(status_code=409, detail="Task name already exists") from exc
        raise

    row = db.fetchone()
    return _task_from_row(db, row)


@router.get("/tasks", response_model=list[SyncTask])
def list_tasks(db=Depends(get_db)):
    db.execute("SELECT * FROM sync_tasks ORDER BY id ASC")
    rows = db.fetchall()
    cols = [d[0] for d in db.description]
    return [SyncTask(**dict(zip(cols, row))) for row in rows]


@router.get("/tasks/{task_id}", response_model=SyncTask)
def get_task(task_id: int, db=Depends(get_db)):
    return _get_task_or_404(task_id, db)


@router.patch("/tasks/{task_id}", response_model=SyncTask)
def patch_task(task_id: int, payload: SyncTaskUpdate, db=Depends(get_db)):
    db.execute("SELECT id, name, type, config FROM sync_tasks WHERE id = %s", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")

    _, curr_name, task_type, curr_config = row
    name = payload.name if payload.name is not None else curr_name
    config = dict(curr_config or {})
    if payload.config:
        config.update(payload.config)
    config = _normalize_config(task_type, config)

    try:
        db.execute(
            """
            UPDATE sync_tasks
            SET name = %s, config = %s::jsonb, updated_at = NOW()
            WHERE id = %s
            RETURNING *
            """,
            (name, json.dumps(config), task_id),
        )
    except Exception as exc:
        msg = str(exc).lower()
        if "duplicate key value" in msg and "sync_tasks_name_key" in msg:
            raise HTTPException(status_code=409, detail="Task name already exists") from exc
        raise

    return _task_from_row(db, db.fetchone())


@router.post("/tasks/{task_id}/start", response_model=SyncTask)
def start_task(task_id: int, db=Depends(get_db)):
    task = _get_task_or_404(task_id, db)
    if task.status == "completed":
        raise HTTPException(status_code=409, detail="Completed task cannot be started")
    if task.desired_status == "running":
        return task
    if _is_transitioning(task):
        raise HTTPException(status_code=409, detail="Task transition in progress")

    db.execute(
        "UPDATE sync_tasks SET desired_status = 'running', updated_at = NOW() WHERE id = %s RETURNING *",
        (task_id,),
    )
    row = db.fetchone()
    return _task_from_row(db, row)


@router.post("/tasks/{task_id}/stop", response_model=SyncTask)
def stop_task(task_id: int, db=Depends(get_db)):
    task = _get_task_or_404(task_id, db)
    if task.desired_status == "stopped":
        return task
    if _is_transitioning(task):
        raise HTTPException(status_code=409, detail="Task transition in progress")

    db.execute(
        "UPDATE sync_tasks SET desired_status = 'stopped', updated_at = NOW() WHERE id = %s RETURNING *",
        (task_id,),
    )
    row = db.fetchone()
    return _task_from_row(db, row)


@router.delete("/tasks/{task_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_task(task_id: int, confirm: bool = Query(False), db=Depends(get_db)):
    if not confirm:
        raise HTTPException(status_code=400, detail="Delete requires confirm=true")

    task = _get_task_or_404(task_id, db)
    if task.status == "running" or task.desired_status == "running":
        raise HTTPException(status_code=409, detail="Stop task before deleting")
    if _is_transitioning(task):
        raise HTTPException(status_code=409, detail="Task transition in progress")

    db.execute("DELETE FROM sync_tasks WHERE id = %s RETURNING id", (task_id,))
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
