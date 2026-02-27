from contextlib import contextmanager

import psycopg2
from psycopg2 import pool
from psycopg2.extras import Json, execute_values

import config

try:
    connection_pool = psycopg2.pool.SimpleConnectionPool(
        1,
        10,
        config.DATABASE_URL,
    )
except Exception as e:
    print(f"Error connecting to database: {e}")
    raise e


@contextmanager
def get_cursor():
    conn = connection_pool.getconn()
    try:
        cur = conn.cursor()
        yield cur, conn
        conn.commit()
    except Exception as e:
        conn.rollback()
        raise e
    finally:
        cur.close()
        connection_pool.putconn(conn)


def _rows_to_dicts(cur, rows: list[tuple]) -> list[dict]:
    cols = [desc[0] for desc in cur.description]
    return [dict(zip(cols, row)) for row in rows]


def upsert_galleries_bulk(rows: list[tuple]) -> int:
    if not rows:
        return 0

    sql = """
        INSERT INTO eh_galleries (
            gid, token, category, title, title_jpn, uploader, posted_at, language,
            pages, rating, fav_count, comment_count, thumb, tags, last_synced_at, is_active
        ) VALUES %s
        ON CONFLICT (gid) DO UPDATE SET
            token = EXCLUDED.token,
            category = EXCLUDED.category,
            title = EXCLUDED.title,
            title_jpn = EXCLUDED.title_jpn,
            uploader = EXCLUDED.uploader,
            posted_at = EXCLUDED.posted_at,
            language = EXCLUDED.language,
            pages = EXCLUDED.pages,
            rating = EXCLUDED.rating,
            fav_count = EXCLUDED.fav_count,
            comment_count = EXCLUDED.comment_count,
            thumb = EXCLUDED.thumb,
            tags = EXCLUDED.tags,
            last_synced_at = NOW(),
            is_active = TRUE
    """
    template = """
        (%s, %s, %s, %s, %s, %s, %s, %s,
         %s, %s, %s, %s, %s, %s, NOW(), TRUE)
    """

    thumb_rows = [(row[0], row[12]) for row in rows if row[12]]

    with get_cursor() as (cur, _):
        execute_values(cur, sql, rows, template=template, page_size=len(rows))

        if thumb_rows:
            execute_values(
                cur,
                """
                INSERT INTO thumb_queue (gid, thumb_url)
                VALUES %s
                ON CONFLICT (gid) DO UPDATE SET
                    thumb_url = EXCLUDED.thumb_url,
                    status = 'pending',
                    retry_count = 0,
                    processed_at = NULL
                WHERE thumb_queue.thumb_url != EXCLUDED.thumb_url
                   OR thumb_queue.status = 'failed'
                """,
                thumb_rows,
                template="(%s, %s)",
                page_size=min(len(thumb_rows), 1000),
            )

    return len(rows)


def count_galleries_by_category(category: str) -> int:
    with get_cursor() as (cur, _):
        cur.execute(
            "SELECT COUNT(*) FROM eh_galleries WHERE LOWER(category) = LOWER(%s)",
            (category,),
        )
        row = cur.fetchone()
        return int(row[0]) if row else 0


def list_sync_tasks() -> list[dict]:
    with get_cursor() as (cur, _):
        cur.execute("SELECT * FROM sync_tasks ORDER BY id ASC")
        rows = cur.fetchall()
        return _rows_to_dicts(cur, rows)


def get_sync_task(task_id: int) -> dict | None:
    with get_cursor() as (cur, _):
        cur.execute("SELECT * FROM sync_tasks WHERE id = %s", (task_id,))
        row = cur.fetchone()
        if not row:
            return None
        cols = [desc[0] for desc in cur.description]
        return dict(zip(cols, row))


def get_task_runtime(task_id: int) -> dict | None:
    with get_cursor() as (cur, _):
        cur.execute(
            """
            SELECT id, name, type, category, desired_status, status, config, state, progress_pct
            FROM sync_tasks
            WHERE id = %s
            """,
            (task_id,),
        )
        row = cur.fetchone()
        if not row:
            return None
        cols = [desc[0] for desc in cur.description]
        return dict(zip(cols, row))


def update_task_runtime(
    task_id: int,
    *,
    state: dict | None = None,
    progress_pct: float | None = None,
    status: str | None = None,
    error_message: str | None = None,
    touch_run_time: bool = False,
) -> None:
    updates = ["updated_at = NOW()"]
    params: list = []

    if state is not None:
        updates.append("state = %s")
        params.append(Json(state))
    if progress_pct is not None:
        updates.append("progress_pct = %s")
        params.append(progress_pct)
    if status is not None:
        updates.append("status = %s")
        params.append(status)
    if error_message is not None:
        updates.append("error_message = %s")
        params.append(error_message)
    if touch_run_time:
        updates.append("last_run_at = NOW()")

    if len(updates) == 1:
        return

    params.append(task_id)
    with get_cursor() as (cur, _):
        cur.execute(
            f"UPDATE sync_tasks SET {', '.join(updates)} WHERE id = %s",
            params,
        )


def set_task_desired_status(task_id: int, desired_status: str) -> None:
    with get_cursor() as (cur, _):
        cur.execute(
            "UPDATE sync_tasks SET desired_status = %s, updated_at = NOW() WHERE id = %s",
            (desired_status, task_id),
        )


def claim_next_thumb_queue_item() -> dict | None:
    with get_cursor() as (cur, _):
        cur.execute(
            """
            UPDATE thumb_queue SET status = 'processing'
            WHERE id = (
                SELECT id
                FROM thumb_queue
                WHERE status = 'pending'
                  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
                ORDER BY created_at
                LIMIT 1
                FOR UPDATE SKIP LOCKED
            )
            RETURNING id, gid, thumb_url, retry_count
            """
        )
        row = cur.fetchone()
        if not row:
            return None
        cols = [desc[0] for desc in cur.description]
        return dict(zip(cols, row))


def mark_thumb_queue_done(item_id: int) -> None:
    with get_cursor() as (cur, _):
        cur.execute(
            """
            UPDATE thumb_queue
            SET status = 'done', processed_at = NOW()
            WHERE id = %s
            """,
            (item_id,),
        )


def mark_thumb_queue_failed(item_id: int) -> tuple[int, str] | None:
    with get_cursor() as (cur, _):
        cur.execute(
            """
            UPDATE thumb_queue
            SET retry_count = retry_count + 1,
                status = 'pending',
                processed_at = NULL,
                next_retry_at = NOW() + (
                    LEAST(POWER(2, retry_count + 1), 8) || ' minutes'
                )::interval
            WHERE id = %s
            RETURNING retry_count, status
            """,
            (item_id,),
        )
        row = cur.fetchone()
        if not row:
            return None
        return row[0], row[1]
