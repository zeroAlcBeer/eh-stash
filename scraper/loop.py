import asyncio
import logging
import re
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

from curl_cffi.requests import AsyncSession
from psycopg2.extras import Json

import config
import db
from parser import GalleryListItem, parse_detail, parse_gallery_list

logger = logging.getLogger(__name__)

# ExHentai f_cats bitmask: each bit = a category to EXCLUDE
_ALL_CATS = 1023
_CATEGORY_BITS = {
    "Misc": 1,
    "Doujinshi": 2,
    "Manga": 4,
    "Artist CG": 8,
    "Game CG": 16,
    "Image Set": 32,
    "Cosplay": 64,
    "Asian Porn": 128,
    "Non-H": 256,
    "Western": 512,
}
VALID_CATEGORIES = set(_CATEGORY_BITS.keys())
MIXED_CATEGORY = "Mixed"

SCHEDULER_POLL_INTERVAL = 3
THUMB_IDLE_SLEEP = 5
WARMUP_DELAY = 30  # 启动后等待秒数再开始调度任务

# ── 全局 IP ban 屏障 ─────────────────────────────────────────────────────────
_ban_until: float = 0.0  # wall-clock timestamp, 0 = 无 ban
_ban_lock = asyncio.Lock() if hasattr(asyncio, 'Lock') else None  # 延迟初始化

_BAN_RE = re.compile(
    r"ban expires in\s+(?:(\d+)\s*hours?)?\s*,?\s*(?:(\d+)\s*minutes?)?\s*(?:and\s+)?(?:(\d+)\s*seconds?)?",
    re.IGNORECASE,
)


def _parse_ban_seconds(text: str) -> int:
    """从 ban 页面 HTML 解析剩余秒数，解析失败默认 300s (5min)"""
    m = _BAN_RE.search(text)
    if not m:
        return 300
    hours = int(m.group(1) or 0)
    mins = int(m.group(2) or 0)
    secs = int(m.group(3) or 0)
    total = hours * 3600 + mins * 60 + secs
    return total if total > 0 else 300


async def _set_ban(seconds: int):
    """设置全局 ban 屏障，所有主站请求将阻塞到 ban 解除"""
    global _ban_until
    _ban_until = time.time() + seconds
    logger.warning(f"[BAN  ] IP banned, all main-site requests paused for {seconds}s (until {time.strftime('%H:%M:%S', time.localtime(_ban_until))})")


async def _wait_if_banned():
    """如果当前处于 ban 状态，阻塞等待直到 ban 解除，解除后额外冷却"""
    global _ban_until
    if _ban_until <= 0:
        return
    remaining = _ban_until - time.time()
    if remaining > 0:
        logger.info(f"[BAN  ] waiting {remaining:.0f}s for ban to expire...")
        await asyncio.sleep(remaining)
        cooldown = config.BAN_COOLDOWN
        logger.info(f"[BAN  ] ban expired, cooling down {cooldown:.0f}s before resuming...")
        await asyncio.sleep(cooldown)
        logger.info("[BAN  ] cooldown complete, resuming requests")
    _ban_until = 0.0


class GlobalRateLimiter:
    """全局速率限制器：保证所有 HTTP 请求之间至少间隔 interval 秒，跨任务共享"""

    def __init__(self, interval: float):
        self._interval = interval
        self._lock = asyncio.Lock()
        self._last_time = 0.0

    async def acquire(self):
        # 先等 ban 解除
        await _wait_if_banned()
        async with self._lock:
            # 二次检查：防止在锁外等待期间其他协程触发了 ban
            if _ban_until > 0:
                await _wait_if_banned()
            now = asyncio.get_event_loop().time()
            wait = self._interval - (now - self._last_time)
            if wait > 0:
                await asyncio.sleep(wait)
            self._last_time = asyncio.get_event_loop().time()


class SimpleRateLimiter:
    """轻量限速器：仅控制请求间隔，不检查 ban 状态（用于 CDN 等独立域名）"""

    def __init__(self, interval: float):
        self._interval = interval
        self._lock = asyncio.Lock()
        self._last_time = 0.0

    async def acquire(self):
        async with self._lock:
            now = asyncio.get_event_loop().time()
            wait = self._interval - (now - self._last_time)
            if wait > 0:
                await asyncio.sleep(wait)
            self._last_time = asyncio.get_event_loop().time()


_rate_limiter: GlobalRateLimiter | None = None
_thumb_rate_limiter: SimpleRateLimiter | None = None
_scorer_reset: asyncio.Event | None = None

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


def init_state(task_type: str, task_config: dict) -> dict:
    if task_type == "full":
        return {
            "next_gid": task_config.get("start_gid"),
            "round": 0,
            "done": False,
            "anchor_gid": None,
            "total_count": None,
        }
    if task_type == "favorites":
        return {"round": 0}
    return {
        "next_gid": None,
        "round": 0,
        "latest_gid": None,
        "scanned_count": 0,
    }


def normalize_full_config(raw: dict | None) -> dict:
    cfg = dict(DEFAULT_FULL_CONFIG)
    cfg.update({k: v for k, v in (raw or {}).items() if k != "inline_set"})
    cfg["inline_set"] = "dm_e"  # 始终写死
    cfg["start_gid"] = cfg.get("start_gid")
    return cfg


def normalize_incremental_config(raw: dict | None) -> dict:
    cfg_raw = dict(raw or {})
    categories = cfg_raw.get("categories")
    if not isinstance(categories, list) or not categories:
        raise ValueError("incremental config.categories must be a non-empty list")

    normalized: list[str] = []
    seen: set[str] = set()
    for item in categories:
        if not isinstance(item, str):
            raise ValueError("incremental config.categories must be a list of strings")
        value = item.strip()
        if value not in VALID_CATEGORIES:
            raise ValueError(f"invalid category '{value}' in incremental config.categories")
        if value not in seen:
            seen.add(value)
            normalized.append(value)

    cfg = dict(DEFAULT_INCREMENTAL_CONFIG)
    cfg["inline_set"] = "dm_e"  # 始终写死
    cfg["categories"] = normalized
    cfg["scan_window"] = int(cfg_raw.get("scan_window") or DEFAULT_INCREMENTAL_CONFIG["scan_window"])
    cfg["rating_diff_threshold"] = float(
        cfg_raw.get("rating_diff_threshold") or DEFAULT_INCREMENTAL_CONFIG["rating_diff_threshold"]
    )
    return cfg


def normalize_favorites_config(raw: dict | None) -> dict:
    cfg = dict(DEFAULT_FAVORITES_CONFIG)
    try:
        cfg["run_interval_hours"] = max(1.0, float((raw or {}).get("run_interval_hours", 6)))
    except (TypeError, ValueError):
        pass
    return cfg


def normalize_full_state(raw: dict | None, task_config: dict) -> dict:
    state = init_state("full", task_config)
    state.update(raw or {})
    return state


def normalize_incremental_state(raw: dict | None) -> dict:
    state = init_state("incremental", {})
    state.update(raw or {})
    return state


def clamp_progress(value: float) -> float:
    return max(0.0, min(100.0, value))


def calc_full_progress(
    db_count: int,
    total_count: int | None,
    done: bool,
) -> float:
    """用「DB 已入库条数 / 列表页 Found about N 总数」精确计算进度"""
    if done:
        return 100.0
    if not total_count or total_count <= 0:
        return 0.0
    return clamp_progress(db_count / total_count * 100)


def calc_incremental_progress(scanned_count: int, scan_window: int) -> float:
    if scan_window <= 0:
        return 100.0
    return clamp_progress(scanned_count / scan_window * 100)


async def validate_access(client: AsyncSession) -> bool:
    url = config.EX_BASE_URL
    logger.info(f"Validating access to {url} ...")

    while True:
        try:
            resp = await client.get(url, timeout=30)
            if resp.status_code != 200:
                logger.error(f"Access check failed: HTTP {resp.status_code}")
                return False

            if "panda.png" in resp.text or "Sad Panda" in resp.text:
                logger.critical("ACCESS DENIED: Sad Panda detected. Check EX_COOKIES.")
                return False

            if "This page requires you to log on" in resp.text or "You must be logged in" in resp.text:
                logger.critical("ACCESS DENIED: Login required. Check EX_COOKIES.")
                return False

            if "temporarily banned" in resp.text or "IP address has been" in resp.text:
                ban_secs = _parse_ban_seconds(resp.text)
                logger.warning(
                    f"[BAN  ] IP banned during startup, waiting {ban_secs}s "
                    f"(until {time.strftime('%H:%M:%S', time.localtime(time.time() + ban_secs))})"
                )
                await asyncio.sleep(ban_secs)
                logger.info("[BAN  ] ban expired, retrying access check...")
                continue

            has_nav = 'id="nb"' in resp.text
            has_gallery = 'class="itg"' in resp.text or "itg glte" in resp.text or "itg gltc" in resp.text

            if not has_nav and not has_gallery:
                logger.critical("ACCESS DENIED: No navigation or gallery found. Cookies likely invalid.")
                return False

            logger.info("Access check passed.")
            return True
        except Exception as e:
            logger.error(f"Access check failed with exception: {e}")
            return False


async def fetch_list_page(
    client: AsyncSession,
    categories: list[str],
    inline_set: str,
    next_cursor: str | None = None,
    task_name: str | None = None,
    category_label: str | None = None,
):
    include_mask = 0
    for category in categories:
        include_mask |= _CATEGORY_BITS[category]
    fcats = _ALL_CATS - include_mask
    label = category_label or ",".join(categories)
    url = f"{config.EX_BASE_URL}/?f_cats={fcats}&inline_set={inline_set}"
    if next_cursor is not None:
        url += f"&next={next_cursor}"
    _tid = f"name={task_name} " if task_name is not None else ""

    try:
        await _rate_limiter.acquire()
        logger.info(f"[LIST ] {_tid}GET {url}")
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"[LIST ] {_tid}{label:<10} HTTP {resp.status_code}")
            return None

        if "panda.png" in resp.text or "Sad Panda" in resp.text:
            logger.error(f"[LIST ] {_tid}Sad Panda detected while fetching list page.")
            return None

        if "This page requires you to log on" in resp.text:
            logger.error(f"[LIST ] {_tid}Login required while fetching list page.")
            return None

        if "temporarily banned" in resp.text or "IP address has been" in resp.text:
            ban_secs = _parse_ban_seconds(resp.text)
            await _set_ban(ban_secs)
            return "BANNED"

        items, next_gid, total_count = parse_gallery_list(resp.text)
        return items, next_gid, total_count
    except Exception as e:
        logger.error(f"[LIST ] {_tid}{label:<10} fetch error: {e}")
        return None


async def fetch_detail(client: AsyncSession, gid: int, token: str, task_name: str | None = None):
    url = f"{config.EX_BASE_URL}/g/{gid}/{token}/"
    _tid = f"[{task_name}] " if task_name is not None else ""
    try:
        await _rate_limiter.acquire()
        logger.info(f"[DETAIL] {_tid}GET {url}")
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"[DETAIL] {_tid}gid={gid} HTTP {resp.status_code}")
            return None
        if "temporarily banned" in resp.text or "IP address has been" in resp.text:
            ban_secs = _parse_ban_seconds(resp.text)
            await _set_ban(ban_secs)
            return "BANNED"
        return parse_detail(resp.text)
    except Exception as e:
        logger.error(f"[DETAIL] {_tid}gid={gid} fetch error: {e}")
        return None


def get_gallery_meta(gid: int):
    with db.get_cursor() as (cur, _):
        cur.execute("SELECT fav_count, rating, tags FROM eh_galleries WHERE gid = %s", (gid,))
        row = cur.fetchone()
        if not row:
            return None

        fav_count, rating, tags = row
        return {
            "fav_count": int(fav_count or 0),
            "rating": float(rating) if rating is not None else None,
            "detail_tags": flatten_detail_tags(tags),
        }


def flatten_detail_tags(tags_obj) -> set[str]:
    if not isinstance(tags_obj, dict):
        return set()
    out: set[str] = set()
    for values in tags_obj.values():
        if isinstance(values, (list, tuple)):
            for value in values:
                text = " ".join(str(value or "").split()).lower()
                if text:
                    out.add(text)
    return out


def bucket_rating(value: float | None) -> float | None:
    if value is None:
        return None
    return round(value * 2.0) / 2.0


def should_refresh_from_list(
    existing: dict,
    item: GalleryListItem,
    threshold: float,
) -> tuple[bool, list[str]]:
    reasons: list[str] = []

    detail_tags: set[str] = existing["detail_tags"]
    list_tags = {tag for tag in item.visible_tags if tag}
    missing_tags = sorted(list_tags - detail_tags)
    tag_ok = not missing_tags
    if missing_tags:
        reasons.append(f"tags_missing:{len(missing_tags)}")

    detail_bucket = bucket_rating(existing["rating"])
    list_bucket = bucket_rating(item.rating_est)

    rating_eq = True
    if detail_bucket is None and list_bucket is not None:
        reasons.append(f"rating:none->{list_bucket:.1f}")
        rating_eq = False
    elif detail_bucket is not None and list_bucket is not None:
        diff = abs(detail_bucket - list_bucket)
        if diff >= threshold:
            reasons.append(f"rating:{detail_bucket:.1f}->{list_bucket:.1f}")
            rating_eq = False

    decision = "refresh" if reasons else "skip"
    if detail_bucket is None and list_bucket is None:
        rating_part = "rating=None==None"
    elif detail_bucket is None:
        rating_part = f"rating=None!={list_bucket:.1f}"
    elif list_bucket is None:
        rating_part = f"rating={detail_bucket:.1f}!=None"
    elif rating_eq:
        rating_part = f"rating={detail_bucket:.1f}=={list_bucket:.1f}"
    else:
        rating_part = f"rating={detail_bucket:.1f}!={list_bucket:.1f}"
    if tag_ok:
        tag_part = f"tag=subset({len(list_tags)}/{len(list_tags)})"
    else:
        tag_part = f"tag=subset({len(list_tags) - len(missing_tags)}/{len(list_tags)})"
    if missing_tags:
        preview = ",".join(missing_tags[:3])
        if len(missing_tags) > 3:
            preview += ",..."
        tag_part += f" missing=[{preview}]"
    logger.info(f"[INCR ] gid={item.gid} {tag_part} {rating_part} → {decision}")

    return bool(reasons), reasons


def build_upsert_row(gid: int, token: str, detail: dict):
    return (
        gid,
        token,
        detail.get("category"),
        detail.get("title"),
        detail.get("title_jpn"),
        detail.get("uploader"),
        detail.get("posted"),
        detail.get("language"),
        detail.get("pages"),
        detail.get("rating"),
        detail.get("fav_count"),
        detail.get("comment_count", 0),
        detail.get("thumb"),
        Json(detail.get("tags", {})),
    )


async def run_full_once(client: AsyncSession, task_id: int, runtime: dict) -> bool:
    cfg = normalize_full_config(runtime.get("config"))
    state = normalize_full_state(runtime.get("state"), cfg)
    _name = runtime["name"]

    if state.get("done") and runtime.get("status") == "completed":
        state = init_state("full", cfg)

    category = runtime["category"]
    next_gid = state.get("next_gid")

    logger.info(f"[FULL ] [{_name}] category={category} fetching next_gid={next_gid}")
    result = await fetch_list_page(
        client,
        [category],
        cfg["inline_set"],
        next_gid,
        task_name=runtime["name"],
        category_label=category,
    )

    if result is None:
        logger.warning(f"[FULL ] [{_name}] fetch_list_page failed, will retry next loop")
        db.update_task_runtime(
            task_id,
            state=state,
            status="running",
            progress_pct=runtime.get("progress_pct") or 0.0,
            error_message="",
            touch_run_time=True,
        )
        return False

    if result == "BANNED":
        ban_msg = "IP temporarily banned by ExHentai, will retry when ban expires"
        logger.warning(f"[FULL ] [{_name}] {ban_msg}")
        db.update_task_runtime(
            task_id,
            state=state,
            status="running",
            progress_pct=runtime.get("progress_pct") or 0.0,
            error_message=ban_msg,
            touch_run_time=False,
        )
        return False

    items, next_cursor, total_count = result

    if items and state.get("anchor_gid") is None:
        state["anchor_gid"] = max(item.gid for item in items)

    # 首次或每页都更新 total_count（取最大值以免估计值缩水）
    if total_count:
        state["total_count"] = max(total_count, state.get("total_count") or 0) or total_count

    logger.info(
        f"[FULL ] [{_name}] category={category} page_items={len(items)}"
        f" next_gid={next_cursor} total_count={state.get('total_count')}"
    )

    rows_to_upsert = []
    for item in items:
        detail = await fetch_detail(client, item.gid, item.token, task_name=runtime["name"])
        if detail == "BANNED":
            ban_msg = "IP temporarily banned by ExHentai, will retry when ban expires"
            logger.warning(f"[FULL ] [{_name}] gid={item.gid} {ban_msg}")
            db.update_task_runtime(
                task_id,
                state=state,
                status="running",
                progress_pct=runtime.get("progress_pct") or 0.0,
                error_message=ban_msg,
                touch_run_time=False,
            )
            return False
        if detail:
            rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))
        else:
            logger.warning(f"[FULL ] [{_name}] gid={item.gid} detail fetch failed, skipping")

    if rows_to_upsert:
        db.upsert_galleries_bulk(rows_to_upsert)

    done = (not items) or (next_cursor is None)
    round_num = int(state.get("round") or 0)

    if done:
        state["next_gid"] = None
        state["round"] = round_num + 1
        state["done"] = True
        progress_pct = 100.0

        db.update_task_runtime(
            task_id,
            state=state,
            progress_pct=progress_pct,
            status="completed",
            error_message="",
            touch_run_time=True,
        )
        db.set_task_desired_status(task_id, "stopped")
        logger.info(f"[FULL ] [{_name}] completed round={round_num + 1}")
        return True

    state["next_gid"] = next_cursor
    state["done"] = False
    db_count = db.count_galleries_by_category(category)
    state["db_count"] = db_count
    progress_pct = calc_full_progress(
        db_count=db_count,
        total_count=state.get("total_count"),
        done=False,
    )
    logger.info(
        f"[FULL ] [{_name}] upserted={len(rows_to_upsert)}"
        f" db_count={db_count} progress={progress_pct:.2f}%"
    )

    db.update_task_runtime(
        task_id,
        state=state,
        progress_pct=progress_pct,
        status="running",
        error_message="",
        touch_run_time=True,
    )
    return False


async def run_incremental_once(client: AsyncSession, task_id: int, runtime: dict) -> bool:
    cycle_cfg = normalize_incremental_config(runtime.get("config"))
    state = normalize_incremental_state(runtime.get("state"))
    _name = runtime["name"]
    task_category = runtime["category"]
    categories = cycle_cfg["categories"]

    cursor = state.get("next_gid")
    round_num = int(state.get("round") or 0)
    latest_gid = state.get("latest_gid")
    scanned_count = int(state.get("scanned_count") or 0)

    scan_window = cycle_cfg["scan_window"]
    rating_threshold = cycle_cfg["rating_diff_threshold"]

    exit_reason = None
    progress_pct = float(runtime.get("progress_pct") or 0.0)

    while True:
        runtime_now = db.get_task_runtime(task_id)
        if not runtime_now:
            return True
        if runtime_now["desired_status"] != "running":
            state["next_gid"] = cursor
            state["latest_gid"] = latest_gid
            state["scanned_count"] = scanned_count
            db.update_task_runtime(
                task_id,
                state=state,
                progress_pct=progress_pct,
                status="stopped",
                touch_run_time=True,
            )
            return True

        page_cfg = normalize_incremental_config(runtime_now.get("config"))
        inline_set = page_cfg["inline_set"]
        categories = page_cfg["categories"]
        rating_threshold = page_cfg["rating_diff_threshold"]

        result = await fetch_list_page(
            client,
            categories,
            inline_set,
            cursor,
            task_name=runtime["name"],
            category_label=f"{task_category}({len(categories)})",
        )

        if result is None:
            logger.warning(f"[INCR ] [{_name}] fetch_list_page failed, cursor={cursor}")
            exit_reason = "ERROR"
            break

        if result == "BANNED":
            logger.warning(f"[INCR ] [{_name}] IP banned, cursor={cursor}")
            exit_reason = "BANNED"
            break

        items, next_cursor, _ = result
        if not items:
            exit_reason = "END"
            break

        if latest_gid is None:
            latest_gid = max(item.gid for item in items)
            logger.info(
                f"[INCR ] [{_name}] cycle start category={task_category}"
                f" categories={categories}"
                f" latest={latest_gid} scan_window={scan_window}"
            )

        logger.debug(
            f"[INCR ] [{_name}] page cursor={cursor} items={len(items)}"
        )

        rows_to_upsert = []
        n_new = n_skip = n_refresh = 0
        for item in items:
            existing = get_gallery_meta(item.gid)
            if existing is None:
                n_new += 1
                detail = await fetch_detail(client, item.gid, item.token, task_name=runtime["name"])
                if detail == "BANNED":
                    logger.warning(f"[INCR ] [{_name}] gid={item.gid} IP banned (new)")
                    exit_reason = "BANNED"
                    break
                if detail:
                    rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))
                continue

            should_fetch, reasons = should_refresh_from_list(existing, item, rating_threshold)
            if not should_fetch:
                n_skip += 1
                continue

            n_refresh += 1
            detail = await fetch_detail(client, item.gid, item.token, task_name=runtime["name"])
            if detail == "BANNED":
                logger.warning(f"[INCR ] [{_name}] gid={item.gid} IP banned (refresh)")
                exit_reason = "BANNED"
                break
            if detail:
                rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))

        # 所有 items 计入已扫描（含 skip/new/refresh）
        scanned_count += len(items)

        logger.info(
            f"[INCR ] [{_name}] page_items={len(items)}"
            f" new={n_new} skip={n_skip} refresh={n_refresh}"
        )

        if rows_to_upsert:
            db.upsert_galleries_bulk(rows_to_upsert)

        if exit_reason == "BANNED":
            break

        cursor = next_cursor
        progress_pct = calc_incremental_progress(scanned_count, scan_window)
        logger.info(
            f"[INCR ] [{_name}] upserted={len(rows_to_upsert)}"
            f" scanned={scanned_count}/{scan_window} progress={progress_pct:.1f}%"
        )

        if cursor is None:
            exit_reason = "END"
            break
        if scanned_count >= scan_window:
            exit_reason = "WINDOW"
            break

        # ---- 逐页持久化中间状态，使前端可实时观察进度 ----
        state["next_gid"] = cursor
        state["latest_gid"] = latest_gid
        state["scanned_count"] = scanned_count
        db.update_task_runtime(
            task_id,
            state=state,
            progress_pct=progress_pct,
            status="running",
            error_message="",
            touch_run_time=True,
        )

    logger.info(f"[INCR ] [{_name}] exit_reason={exit_reason} round={round_num}")

    if exit_reason in ("END", "WINDOW"):
        new_state = {
            "next_gid": None,
            "round": round_num + 1,
            "latest_gid": None,
            "scanned_count": 0,
        }
        db.update_task_runtime(
            task_id,
            state=new_state,
            progress_pct=0.0,
            status="running",
            error_message="",
            touch_run_time=True,
        )
        return False

    state["next_gid"] = cursor
    state["round"] = round_num
    state["latest_gid"] = latest_gid
    state["scanned_count"] = scanned_count
    ban_msg = "IP temporarily banned by ExHentai, will retry when ban expires"
    db.update_task_runtime(
        task_id,
        state=state,
        progress_pct=progress_pct,
        status="running",
        error_message=ban_msg if exit_reason == "BANNED" else ("incremental fetch error" if exit_reason == "ERROR" else ""),
        touch_run_time=exit_reason != "BANNED",
    )
    return False


# ── Favorites sync ────────────────────────────────────────────────────────────


async def fetch_favorites_list_page(
    client: AsyncSession,
    next_cursor: str | None = None,
    task_name: str | None = None,
):
    """Fetch a single page from favorites.php using cursor-based pagination."""
    url = f"{config.EX_BASE_URL}/favorites.php?inline_set=dm_e"
    if next_cursor is not None:
        url += f"&next={next_cursor}"
    _tid = f"[{task_name}] " if task_name else ""
    try:
        await _rate_limiter.acquire()
        logger.info(f"[FAV  ] {_tid}GET {url}")
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"[FAV  ] {_tid}HTTP {resp.status_code}")
            return None
        if "panda.png" in resp.text or "Sad Panda" in resp.text:
            logger.error(f"[FAV  ] {_tid}Sad Panda detected")
            return None
        if "This page requires you to log on" in resp.text:
            logger.error(f"[FAV  ] {_tid}Login required")
            return None
        if "temporarily banned" in resp.text or "IP address has been" in resp.text:
            ban_secs = _parse_ban_seconds(resp.text)
            await _set_ban(ban_secs)
            return "BANNED"
        items, next_cursor, total_count = parse_gallery_list(resp.text)
        return items, next_cursor, total_count
    except Exception as e:
        logger.error(f"[FAV  ] {_tid}fetch error: {e}")
        return None


async def run_favorites_once(client: AsyncSession, task_id: int, runtime: dict) -> bool:
    """Run a single favorites sync cycle.

    Per-page loop: fetch list → backfill missing details → upsert favorites → rebuild preferences.
    After full traversal: cleanup stale favorites → final rebuild → mark completed.
    On interrupt: persist next_gid to state for resumption.
    """
    _name = runtime["name"]
    state = runtime.get("state") or {}
    round_num = int(state.get("round") or 0)
    cursor = state.get("next_gid")
    is_resuming = cursor is not None
    collected_gids: list[int] = []
    failed_gids: set[int] = set()  # 404 / fetch-failed gids, skip on future pages
    page_num = 0

    while True:
        # ── Check desired_status (support interrupt) ──
        runtime_now = db.get_task_runtime(task_id)
        if not runtime_now:
            return True
        if runtime_now["desired_status"] != "running":
            db.update_task_runtime(
                task_id, status="stopped",
                state={"next_gid": cursor, "round": round_num},
                touch_run_time=True,
            )
            logger.info(f"[FAV  ] [{_name}] stop requested, saved cursor={cursor}")
            return True

        # ── Fetch one page ──
        result = await fetch_favorites_list_page(client, cursor, task_name=_name)

        if result is None:
            logger.warning(f"[FAV  ] [{_name}] fetch failed at cursor={cursor}")
            db.update_task_runtime(
                task_id, status="running",
                state={"next_gid": cursor, "round": round_num},
                error_message="favorites page fetch failed",
            )
            return False

        if result == "BANNED":
            db.update_task_runtime(
                task_id, status="running",
                state={"next_gid": cursor, "round": round_num},
                error_message="IP temporarily banned by ExHentai, will retry when ban expires",
            )
            return False

        items, next_cursor, _ = result
        page_num += 1

        if not items:
            logger.info(f"[FAV  ] [{_name}] empty page at cursor={cursor}, end of favorites")
            break

        page_gids = [item.gid for item in items]
        logger.info(f"[FAV  ] [{_name}] page {page_num}: items={len(items)} next_gid={next_cursor}")

        # ── Backfill missing galleries ──
        missing_gids = [g for g in db.get_non_existing_gids(page_gids) if g not in failed_gids]
        if missing_gids:
            item_map = {item.gid: item for item in items}
            logger.info(f"[FAV  ] [{_name}] {len(missing_gids)} galleries need detail fetch")
            rows_to_upsert = []
            for gid in missing_gids:
                item = item_map.get(gid)
                if not item:
                    continue
                detail = await fetch_detail(client, gid, item.token, task_name=_name)
                if detail == "BANNED":
                    db.update_task_runtime(
                        task_id, status="running",
                        state={"next_gid": cursor, "round": round_num},
                        error_message="IP temporarily banned during detail backfill",
                    )
                    return False
                if detail:
                    rows_to_upsert.append(build_upsert_row(gid, item.token, detail))
                else:
                    failed_gids.add(gid)
                    logger.info(f"[FAV  ] [{_name}] gid={gid} detail failed, skipping")
            if rows_to_upsert:
                db.upsert_galleries_bulk(rows_to_upsert)
                logger.info(f"[FAV  ] [{_name}] backfilled {len(rows_to_upsert)} galleries")

        # ── Upsert favorites (per-page) ──
        fav_pairs = [(item.gid, item.favorited_at) for item in items]
        fav_count = db.upsert_favorites(fav_pairs)
        logger.info(f"[FAV  ] [{_name}] upserted {fav_count} favorites")

        # ── Rebuild preference tags (per-page) ──
        tag_count = db.rebuild_preference_tags()
        logger.info(f"[FAV  ] [{_name}] rebuilt {tag_count} preference tags")
        if _scorer_reset:
            _scorer_reset.set()

        collected_gids.extend(page_gids)

        # ── Update progress & advance cursor ──
        db.update_task_runtime(
            task_id, status="running", error_message="",
            state={"next_gid": next_cursor, "round": round_num},
        )
        cursor = next_cursor
        if cursor is None:
            break

    # ── Full traversal completed ──
    if not is_resuming and collected_gids:
        removed = db.cleanup_stale_favorites(collected_gids)
        if removed:
            logger.info(f"[FAV  ] [{_name}] cleanup: removed {removed} stale favorites")
            tag_count = db.rebuild_preference_tags()
            logger.info(f"[FAV  ] [{_name}] rebuilt {tag_count} preference tags after cleanup")
            if _scorer_reset:
                _scorer_reset.set()

    logger.info(f"[FAV  ] [{_name}] completed round={round_num + 1}, total={len(collected_gids)} favorites")
    db.update_task_runtime(
        task_id,
        state={"next_gid": None, "round": round_num + 1},
        status="completed",
        progress_pct=100.0,
        error_message="",
        touch_run_time=True,
    )
    return True


async def run_task(client: AsyncSession, task_id: int):
    logger.info(f"[TASK ] start id={task_id}")

    try:
        while True:
            runtime = db.get_task_runtime(task_id)
            if not runtime:
                logger.info(f"[TASK ] id={task_id} deleted")
                return
            _name = runtime["name"]

            if runtime["desired_status"] != "running":
                if runtime["status"] not in ("completed", "error"):
                    db.update_task_runtime(task_id, status="stopped", touch_run_time=True)
                logger.info(f"[TASK ] [{_name}] stop requested")
                return

            if runtime["type"] == "full":
                category = runtime.get("category", "")
                if category not in VALID_CATEGORIES:
                    msg = f"category '{category}' is not a valid ExHentai category. Valid: {sorted(VALID_CATEGORIES)}"
                    logger.error(f"[TASK ] [{_name}] {msg}")
                    db.update_task_runtime(task_id, status="error", error_message=msg, touch_run_time=True)
                    db.set_task_desired_status(task_id, "stopped")
                    return
            elif runtime["type"] == "favorites":
                pass  # No special validation needed
            else:
                category = runtime.get("category", "")
                if category != MIXED_CATEGORY:
                    msg = f"incremental task category must be '{MIXED_CATEGORY}', got '{category}'"
                    logger.error(f"[TASK ] [{_name}] {msg}")
                    db.update_task_runtime(task_id, status="error", error_message=msg, touch_run_time=True)
                    db.set_task_desired_status(task_id, "stopped")
                    return
                try:
                    normalize_incremental_config(runtime.get("config"))
                except Exception as e:
                    msg = f"invalid incremental config: {e}"
                    logger.error(f"[TASK ] [{_name}] {msg}")
                    db.update_task_runtime(task_id, status="error", error_message=msg, touch_run_time=True)
                    db.set_task_desired_status(task_id, "stopped")
                    return

            db.update_task_runtime(task_id, status="running", error_message="", touch_run_time=True)

            if runtime["type"] == "full":
                done = await run_full_once(client, task_id, runtime)
                if done:
                    return
            elif runtime["type"] == "favorites":
                done = await run_favorites_once(client, task_id, runtime)
                if done:
                    return
            else:
                finished = await run_incremental_once(client, task_id, runtime)
                if finished:
                    return

            await asyncio.sleep(0)
    except asyncio.CancelledError:
        runtime = db.get_task_runtime(task_id)
        if runtime and runtime["status"] not in ("completed", "error"):
            db.update_task_runtime(task_id, status="stopped", touch_run_time=True)
        raise


async def run_thumb_worker():
    thumb_dir = Path(config.THUMB_DIR)
    thumb_dir.mkdir(parents=True, exist_ok=True)

    # 启动时清理上次运行残留的 processing 行（进程异常退出时可能遗留）
    reset_count = db.reset_stale_thumb_processing()
    if reset_count:
        logger.info(f"[THUMB] reset {reset_count} stale processing items to pending")
    logger.info(f"[THUMB] worker started, dir={thumb_dir}")

    async with AsyncSession(
        headers=config.HEADERS,
        cookies=config.COOKIES,
        allow_redirects=True,
        timeout=15,
        impersonate="chrome",
        verify=False,
        proxies=config.PROXIES,
    ) as client:
        while True:
            try:
                # 先 acquire 再 claim：避免 claim 后在 limiter 中阻塞导致 processing 卡死
                await _thumb_rate_limiter.acquire()

                item = db.claim_next_thumb_queue_item()
                if not item:
                    await asyncio.sleep(THUMB_IDLE_SLEEP)
                    continue

                item_id = item["id"]
                gid = item["gid"]
                thumb_url = item["thumb_url"]

                try:
                    resp = await client.get(
                        thumb_url,
                        timeout=15,
                        headers={"Referer": "https://exhentai.org/"},
                    )
                    if resp.status_code == 200:
                        (thumb_dir / str(gid)).write_bytes(resp.content)
                        db.mark_thumb_queue_done(item_id)
                    elif resp.status_code == 404:
                        db.mark_thumb_queue_permanent_failed(item_id)
                        logger.warning(f"[THUMB] gid={gid} HTTP 404, marked as failed (no retry)")
                    else:
                        retry_info = db.mark_thumb_queue_failed(item_id)
                        logger.warning(
                            f"[THUMB] gid={gid} HTTP {resp.status_code}, retry={retry_info}"
                        )
                except Exception as e:
                    retry_info = db.mark_thumb_queue_failed(item_id)
                    logger.warning(f"[THUMB] gid={gid} download error={e}, retry={retry_info}")

            except asyncio.CancelledError:
                raise
            except Exception as e:
                logger.error(f"[THUMB] loop error: {e}")
                await asyncio.sleep(10)


SCORER_BATCH_SIZE = 100
SCORER_BATCH_INTERVAL = 0.1  # seconds between batches (pure DB, no rate limit needed)
SCORER_IDLE_INTERVAL = 30    # seconds to wait when fully scored before rechecking


async def run_recommended_scorer():
    """Background coroutine that incrementally computes recommended_cache.
    Processes galleries in batches of SCORER_BATCH_SIZE, gid DESC.
    Resets to latest when _scorer_reset event is set (preference_tags changed).
    """
    global _scorer_reset
    cursor: int | None = None

    logger.info("[SCORE] recommended scorer started")

    while True:
        try:
            # Check for reset signal (non-blocking)
            if _scorer_reset and _scorer_reset.is_set():
                _scorer_reset.clear()
                cursor = None
                logger.info("[SCORE] reset: restarting from latest gid")

            gids, next_cursor = db.score_recommended_batch(cursor, SCORER_BATCH_SIZE)

            if not gids:
                # No more galleries to process or no preference data
                if cursor is not None:
                    logger.info("[SCORE] full scan complete, idling")
                    cursor = None
                # Wait for reset or periodic recheck
                if _scorer_reset:
                    try:
                        await asyncio.wait_for(_scorer_reset.wait(), timeout=SCORER_IDLE_INTERVAL)
                        _scorer_reset.clear()
                        cursor = None
                        logger.info("[SCORE] reset: restarting from latest gid")
                    except asyncio.TimeoutError:
                        pass
                else:
                    await asyncio.sleep(SCORER_IDLE_INTERVAL)
                continue

            cursor = next_cursor
            await asyncio.sleep(SCORER_BATCH_INTERVAL)

        except asyncio.CancelledError:
            raise
        except Exception as e:
            logger.error(f"[SCORE] error: {e}")
            await asyncio.sleep(10)


async def run_loop():
    logger.info("Starting DB-driven sync scheduler...")

    if any("YOUR_" in v for v in config.COOKIES.values()):
        logger.warning("Default cookies detected in .env, update EX_COOKIES.")

    global _rate_limiter, _thumb_rate_limiter, _scorer_reset
    _rate_limiter = GlobalRateLimiter(config.RATE_INTERVAL)
    _thumb_rate_limiter = SimpleRateLimiter(config.THUMB_RATE_INTERVAL)
    _scorer_reset = asyncio.Event()
    logger.info(f"Global rate limiter: main={config.RATE_INTERVAL}s/req  thumb={config.THUMB_RATE_INTERVAL}s/req")
    if config.PROXY_URL:
        logger.info(f"Proxy enabled: {config.PROXY_URL}")
    else:
        logger.info("Proxy disabled (PROXY_URL not set)")

    running_tasks: dict[int, asyncio.Task] = {}

    async with AsyncSession(
        headers=config.HEADERS,
        cookies=config.COOKIES,
        allow_redirects=True,
        timeout=30,
        impersonate="chrome",
        verify=False,
        proxies=config.PROXIES,
    ) as client:
        if not await validate_access(client):
            logger.critical("Startup validation failed. Exiting.")
            sys.exit(1)

        thumb_task = asyncio.create_task(run_thumb_worker(), name="thumb-worker")
        scorer_task = asyncio.create_task(run_recommended_scorer(), name="recommended-scorer")

        logger.info(f"[SCHED] warmup: waiting {WARMUP_DELAY}s before starting task scheduler...")
        await asyncio.sleep(WARMUP_DELAY)
        logger.info("[SCHED] warmup complete, starting task scheduler")

        try:
            while True:
                for task_id, task_obj in list(running_tasks.items()):
                    if not task_obj.done():
                        continue
                    try:
                        task_obj.result()
                    except asyncio.CancelledError:
                        logger.info(f"[TASK ] id={task_id} cancelled")
                    except Exception as e:
                        logger.error(f"[TASK ] id={task_id} crashed: {e}")
                        db.update_task_runtime(task_id, status="error", error_message=str(e), touch_run_time=True)
                        db.set_task_desired_status(task_id, "stopped")
                    finally:
                        running_tasks.pop(task_id, None)

                task_rows = db.list_sync_tasks()
                db_task_map = {row["id"]: row for row in task_rows}

                # DB 已删除的任务：取消内存中的协程
                for task_id, task_obj in running_tasks.items():
                    if task_id not in db_task_map and not task_obj.done():
                        task_obj.cancel()

                for row in task_rows:
                    task_id = row["id"]
                    desired = row["desired_status"]
                    task_obj = running_tasks.get(task_id)

                    if desired == "running":
                        if task_obj is None:
                            # Favorites tasks: wait for run_interval_hours between runs
                            if row["type"] == "favorites" and row["status"] == "completed":
                                interval_hours = (row.get("config") or {}).get("run_interval_hours", 6)
                                last_run = row.get("last_run_at")
                                if last_run:
                                    elapsed = datetime.now(timezone.utc) - last_run
                                    if elapsed < timedelta(hours=interval_hours):
                                        continue
                            running_tasks[task_id] = asyncio.create_task(
                                run_task(client, task_id),
                                name=f"sync-task-{task_id}",
                            )
                    else:
                        if task_obj and not task_obj.done():
                            task_obj.cancel()

                await asyncio.sleep(SCHEDULER_POLL_INTERVAL)
        finally:
            scorer_task.cancel()
            thumb_task.cancel()
            for task_obj in running_tasks.values():
                task_obj.cancel()
            await asyncio.gather(thumb_task, *running_tasks.values(), return_exceptions=True)
