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


def mark_thumb_queue_failed(item_id: int, max_retries: int = 10) -> tuple[int, str] | None:
    with get_cursor() as (cur, _):
        cur.execute(
            """
            UPDATE thumb_queue
            SET retry_count = retry_count + 1,
                status = CASE WHEN retry_count + 1 >= %s THEN 'failed' ELSE 'pending' END,
                processed_at = CASE WHEN retry_count + 1 >= %s THEN NOW() ELSE NULL END,
                next_retry_at = CASE WHEN retry_count + 1 >= %s THEN NULL
                    ELSE NOW() + (LEAST(POWER(2, retry_count + 1), 8) || ' minutes')::interval
                END
            WHERE id = %s
            RETURNING retry_count, status
            """,
            (max_retries, max_retries, max_retries, item_id),
        )
        row = cur.fetchone()
        if not row:
            return None
        return row[0], row[1]


def mark_thumb_queue_permanent_failed(item_id: int) -> None:
    """将缩略图任务标记为永久失败（如 404），不再重试"""
    with get_cursor() as (cur, _):
        cur.execute(
            """
            UPDATE thumb_queue
            SET status = 'failed', processed_at = NOW()
            WHERE id = %s
            """,
            (item_id,),
        )


def reset_stale_thumb_processing() -> int:
    """将所有卡在 processing 状态的缩略图任务重置为 pending（用于启动时清理）"""
    with get_cursor() as (cur, _):
        cur.execute(
            """
            UPDATE thumb_queue
            SET status = 'pending'
            WHERE status = 'processing'
            """
        )
        return cur.rowcount


# ── Favorites & Preferences ──────────────────────────────────────────────────


def upsert_favorites(favorites: list[tuple[int, str | None]]) -> int:
    """Insert new favorites (additive only, called per-page).
    Only inserts gids that exist in eh_galleries (FK safety).
    favorites: list of (gid, favorited_at_str) tuples.
    """
    if not favorites:
        return 0
    gids = [f[0] for f in favorites]
    fav_map = {f[0]: f[1] for f in favorites}
    with get_cursor() as (cur, _):
        cur.execute(
            """
            INSERT INTO user_favorites (gid, favorited_at)
            SELECT v.gid,
                   COALESCE((ts.val)::timestamptz, NOW())
            FROM unnest(%(gids)s::bigint[]) AS v(gid)
            JOIN eh_galleries g ON g.gid = v.gid
            LEFT JOIN (
                SELECT * FROM unnest(%(gids)s::bigint[], %(ts)s::text[])
                    AS t(gid, val)
            ) ts ON ts.gid = v.gid
            ON CONFLICT (gid) DO UPDATE SET favorited_at = EXCLUDED.favorited_at
            """,
            {
                "gids": gids,
                "ts": [fav_map.get(gid) for gid in gids],
            },
        )
        return cur.rowcount


def cleanup_stale_favorites(all_gids: list[int]) -> int:
    """Remove user-canceled favorites (gallery still active but no longer in favorites).
    Called once after a full traversal of all favorites pages.
    """
    if not all_gids:
        with get_cursor() as (cur, _):
            cur.execute(
                "DELETE FROM user_favorites "
                "WHERE gid IN (SELECT gid FROM eh_galleries WHERE is_active = TRUE)"
            )
            return cur.rowcount

    with get_cursor() as (cur, _):
        cur.execute(
            """
            DELETE FROM user_favorites
            WHERE gid NOT IN (SELECT unnest(%(gids)s::bigint[]))
              AND gid IN (SELECT gid FROM eh_galleries WHERE is_active = TRUE)
            """,
            {"gids": all_gids},
        )
        return cur.rowcount


def get_non_existing_gids(gids: list[int]) -> list[int]:
    """Find gids not present in eh_galleries."""
    if not gids:
        return []
    with get_cursor() as (cur, _):
        cur.execute(
            """
            SELECT v.gid
            FROM unnest(%(gids)s::bigint[]) AS v(gid)
            LEFT JOIN eh_galleries g ON g.gid = v.gid
            WHERE g.gid IS NULL
            """,
            {"gids": gids},
        )
        return [row[0] for row in cur.fetchall()]


def rebuild_preference_tags() -> int:
    """Rebuild preference_tags from current user_favorites.

    weight = (1 + ln(TF)) × ln(N / df)   (sublinear TF-IDF)
    count  = raw occurrence count in favorites (pure TF)
    """
    with get_cursor() as (cur, _):
        cur.execute("TRUNCATE preference_tags")
        cur.execute(
            """
            WITH fav_tf AS (
                SELECT ns, tag_value, COUNT(*)::REAL AS tf
                FROM eh_galleries g
                JOIN user_favorites f ON g.gid = f.gid,
                     jsonb_each(g.tags) AS t(ns, vals),
                     jsonb_array_elements_text(vals) AS tag_value
                WHERE ns IN ('artist', 'group', 'character', 'parody')
                GROUP BY ns, tag_value
            ),
            doc_freq AS (
                SELECT ns, tag_value, COUNT(DISTINCT g.gid)::REAL AS df
                FROM eh_galleries g,
                     jsonb_each(g.tags) AS t(ns, vals),
                     jsonb_array_elements_text(vals) AS tag_value
                WHERE g.is_active = TRUE
                  AND ns IN ('artist', 'group', 'character', 'parody')
                GROUP BY ns, tag_value
            ),
            total AS (
                SELECT COUNT(*)::REAL AS n FROM eh_galleries WHERE is_active = TRUE
            )
            INSERT INTO preference_tags (namespace, tag, weight, count)
            SELECT f.ns, f.tag_value,
                   (1.0 + LN(f.tf)) * LN(total.n / GREATEST(d.df, 1)),
                   f.tf
            FROM fav_tf f
            JOIN doc_freq d ON d.ns = f.ns AND d.tag_value = f.tag_value
            CROSS JOIN total
            """
        )
        return cur.rowcount


def score_recommended_batch(cursor_gid: int | None, batch_size: int = 100) -> tuple[list[int], int | None]:
    """Score a batch of galleries and upsert into recommended_cache.
    Processes galleries in gid DESC order starting after cursor_gid.
    Returns (scored_gids, next_cursor_gid) where next_cursor_gid is None when done.
    """
    with get_cursor() as (cur, _):
        # Check if preference_tags has data
        cur.execute("SELECT 1 FROM preference_tags LIMIT 1")
        if not cur.fetchone():
            return [], None

        if cursor_gid is None:
            cur.execute(
                "SELECT gid FROM eh_galleries WHERE is_active = TRUE ORDER BY gid DESC LIMIT %s",
                (batch_size,),
            )
        else:
            cur.execute(
                "SELECT gid FROM eh_galleries WHERE is_active = TRUE AND gid < %s ORDER BY gid DESC LIMIT %s",
                (cursor_gid, batch_size),
            )
        gids = [row[0] for row in cur.fetchall()]
        if not gids:
            return [], None

        cur.execute(
            """
            INSERT INTO recommended_cache (gid, rec_score)
            SELECT g.gid, SUM(p.weight)
            FROM preference_tags p
            JOIN eh_galleries g
              ON g.tags @> jsonb_build_object(p.namespace, jsonb_build_array(p.tag))
            WHERE g.gid = ANY(%(gids)s)
            GROUP BY g.gid
            HAVING SUM(p.weight) >= 20
            ON CONFLICT (gid) DO UPDATE SET rec_score = EXCLUDED.rec_score
            """,
            {"gids": gids},
        )
        scored = cur.rowcount

        # Remove gids that no longer meet threshold
        cur.execute(
            """
            DELETE FROM recommended_cache
            WHERE gid = ANY(%(gids)s)
              AND gid NOT IN (
                  SELECT g.gid
                  FROM preference_tags p
                  JOIN eh_galleries g
                    ON g.tags @> jsonb_build_object(p.namespace, jsonb_build_array(p.tag))
                  WHERE g.gid = ANY(%(gids)s)
                  GROUP BY g.gid
                  HAVING SUM(p.weight) >= 20
              )
            """,
            {"gids": gids},
        )

        next_cursor = gids[-1] if len(gids) == batch_size else None
        return gids, next_cursor
