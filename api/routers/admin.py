import json
from typing import Any, Dict

from fastapi import APIRouter, Depends, HTTPException, status

from db import get_db
from models import (
    SyncTask,
    SyncTaskCreate,
    SyncTaskUpdate,
    ThumbQueueStats,
)

router = APIRouter(prefix="/v1/admin", tags=["admin"])

DEFAULT_FULL_CONFIG = {
    "inline_set": "dm_l",
    "start_gid": None,
}

DEFAULT_INCREMENTAL_CONFIG = {
    "inline_set": "dm_l",
    "detail_quota": 25,
    "gid_window": 10000,
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
        "cutoff_gid": None,
    }


def _normalize_config(task_type: str, config: Dict[str, Any]) -> Dict[str, Any]:
    base = DEFAULT_FULL_CONFIG if task_type == "full" else DEFAULT_INCREMENTAL_CONFIG
    merged = dict(base)
    merged.update({k: v for k, v in (config or {}).items() if k != "inline_set"})
    merged["inline_set"] = "dm_l"  # 始终写死，不允许覆盖
    return merged


def _task_from_row(db, row) -> SyncTask:
    cols = [d[0] for d in db.description]
    item = dict(zip(cols, row))
    return SyncTask(**item)


@router.post("/tasks", response_model=SyncTask, status_code=status.HTTP_201_CREATED)
def create_task(payload: SyncTaskCreate, db=Depends(get_db)):
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
    db.execute("SELECT * FROM sync_tasks WHERE id = %s", (task_id,))
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return _task_from_row(db, row)


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


@router.post("/tasks/{task_id}/start")
def start_task(task_id: int, db=Depends(get_db)):
    db.execute(
        "UPDATE sync_tasks SET desired_status = 'running', updated_at = NOW() WHERE id = %s RETURNING id, desired_status",
        (task_id,),
    )
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return {"id": row[0], "desired_status": row[1]}


@router.post("/tasks/{task_id}/stop")
def stop_task(task_id: int, db=Depends(get_db)):
    db.execute(
        "UPDATE sync_tasks SET desired_status = 'stopped', updated_at = NOW() WHERE id = %s RETURNING id, desired_status",
        (task_id,),
    )
    row = db.fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Task not found")
    return {"id": row[0], "desired_status": row[1]}


@router.delete("/tasks/{task_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_task(task_id: int, db=Depends(get_db)):
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
