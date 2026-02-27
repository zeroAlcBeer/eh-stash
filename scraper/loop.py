import asyncio
import logging
import re
import sys
import time
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
    """如果当前处于 ban 状态，阻塞等待直到 ban 解除"""
    global _ban_until
    if _ban_until <= 0:
        return
    remaining = _ban_until - time.time()
    if remaining > 0:
        logger.info(f"[BAN  ] waiting {remaining:.0f}s for ban to expire...")
        await asyncio.sleep(remaining)
        logger.info("[BAN  ] ban expired, resuming requests")
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
            now = asyncio.get_event_loop().time()
            wait = self._interval - (now - self._last_time)
            if wait > 0:
                await asyncio.sleep(wait)
            self._last_time = asyncio.get_event_loop().time()


_rate_limiter: GlobalRateLimiter | None = None
_thumb_rate_limiter: GlobalRateLimiter | None = None

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


def init_state(task_type: str, task_config: dict) -> dict:
    if task_type == "full":
        return {
            "next_gid": task_config.get("start_gid"),
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


def normalize_full_config(raw: dict | None) -> dict:
    cfg = dict(DEFAULT_FULL_CONFIG)
    cfg.update({k: v for k, v in (raw or {}).items() if k != "inline_set"})
    cfg["inline_set"] = "dm_l"  # 始终写死
    cfg["start_gid"] = cfg.get("start_gid")
    return cfg


def normalize_incremental_config(raw: dict | None) -> dict:
    cfg = dict(DEFAULT_INCREMENTAL_CONFIG)
    cfg.update({k: v for k, v in (raw or {}).items() if k != "inline_set"})
    cfg["inline_set"] = "dm_l"  # 始终写死
    cfg["detail_quota"] = int(cfg.get("detail_quota") or 25)
    cfg["gid_window"] = int(cfg.get("gid_window") or 10000)
    cfg["rating_diff_threshold"] = float(cfg.get("rating_diff_threshold") or 0.5)
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


def calc_incremental_progress(latest_gid: int | None, cutoff_gid: int | None, cursor_gid: int | None) -> float:
    if latest_gid is None or cutoff_gid is None or cursor_gid is None:
        return 0.0
    total = latest_gid - cutoff_gid
    if total <= 0:
        return 100.0
    pct = ((latest_gid - cursor_gid) / total) * 100
    return clamp_progress(pct)


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
    category: str,
    inline_set: str,
    next_gid: int | None = None,
):
    fcats = _ALL_CATS - _CATEGORY_BITS[category]
    url = f"{config.EX_BASE_URL}/?f_cats={fcats}&inline_set={inline_set}"
    if next_gid is not None:
        url += f"&next={next_gid}"

    try:
        await _rate_limiter.acquire()
        logger.info(f"[LIST ] GET {url}")
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"[LIST ] {category:<10} HTTP {resp.status_code}")
            return None

        if "panda.png" in resp.text or "Sad Panda" in resp.text:
            logger.error("Sad Panda detected while fetching list page.")
            return None

        if "This page requires you to log on" in resp.text:
            logger.error("Login required while fetching list page.")
            return None

        if "temporarily banned" in resp.text or "IP address has been" in resp.text:
            ban_secs = _parse_ban_seconds(resp.text)
            await _set_ban(ban_secs)
            return "BANNED"

        items, next_gid, total_count = parse_gallery_list(resp.text)
        return items, next_gid, total_count
    except Exception as e:
        logger.error(f"[LIST ] {category:<10} fetch error: {e}")
        return None


async def fetch_detail(client: AsyncSession, gid: int, token: str):
    url = f"{config.EX_BASE_URL}/g/{gid}/{token}/"
    try:
        await _rate_limiter.acquire()
        logger.info(f"[DETAIL] GET {url}")
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"[DETAIL] gid={gid} HTTP {resp.status_code}")
            return None
        if "temporarily banned" in resp.text or "IP address has been" in resp.text:
            ban_secs = _parse_ban_seconds(resp.text)
            await _set_ban(ban_secs)
            return "BANNED"
        return parse_detail(resp.text)
    except Exception as e:
        logger.error(f"[DETAIL] gid={gid} fetch error: {e}")
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
            "tag_count": count_detail_tags(tags),
        }


def count_detail_tags(tags_obj) -> int:
    if not isinstance(tags_obj, dict):
        return 0
    total = 0
    for values in tags_obj.values():
        if isinstance(values, (list, tuple)):
            total += len(values)
    return total


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

    if item.visible_tag_count != existing["tag_count"]:
        reasons.append(f"tags:{existing['tag_count']}->{item.visible_tag_count}")

    detail_bucket = bucket_rating(existing["rating"])
    list_bucket = bucket_rating(item.rating_est)

    if detail_bucket is None and list_bucket is not None:
        reasons.append(f"rating:none->{list_bucket:.1f}")
    elif detail_bucket is not None and list_bucket is not None:
        diff = abs(detail_bucket - list_bucket)
        if diff >= threshold:
            reasons.append(f"rating:{detail_bucket:.1f}->{list_bucket:.1f}")

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

    if state.get("done") and runtime.get("status") == "completed":
        state = init_state("full", cfg)

    category = runtime["category"]
    next_gid = state.get("next_gid")

    logger.info(f"[FULL ] id={task_id} category={category} fetching next_gid={next_gid}")
    result = await fetch_list_page(client, category, cfg["inline_set"], next_gid)

    if result is None:
        logger.warning(f"[FULL ] id={task_id} fetch_list_page failed, will retry next loop")
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
        logger.warning(f"[FULL ] id={task_id} {ban_msg}")
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
        f"[FULL ] id={task_id} category={category} page_items={len(items)}"
        f" next_gid={next_cursor} total_count={state.get('total_count')}"
    )

    rows_to_upsert = []
    for item in items:
        detail = await fetch_detail(client, item.gid, item.token)
        if detail == "BANNED":
            ban_msg = "IP temporarily banned by ExHentai, will retry when ban expires"
            logger.warning(f"[FULL ] id={task_id} gid={item.gid} {ban_msg}")
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
            logger.warning(f"[FULL ] id={task_id} gid={item.gid} detail fetch failed, skipping")

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
        logger.info(f"[FULL ] id={task_id} completed round={round_num + 1}")
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
        f"[FULL ] id={task_id} upserted={len(rows_to_upsert)}"
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
    category = runtime["category"]

    cursor = state.get("next_gid")
    round_num = int(state.get("round") or 0)
    latest_gid = state.get("latest_gid")
    cutoff_gid = state.get("cutoff_gid")

    if cursor is not None and (latest_gid is None or cutoff_gid is None):
        cursor = None
        latest_gid = None
        cutoff_gid = None

    quota = cycle_cfg["detail_quota"]
    cycle_gid_window = cycle_cfg["gid_window"]
    rating_threshold = cycle_cfg["rating_diff_threshold"]

    exit_reason = "QUOTA"
    progress_pct = float(runtime.get("progress_pct") or 0.0)

    while quota > 0:
        runtime_now = db.get_task_runtime(task_id)
        if not runtime_now:
            return True
        if runtime_now["desired_status"] != "running":
            state["next_gid"] = cursor
            state["latest_gid"] = latest_gid
            state["cutoff_gid"] = cutoff_gid
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
        rating_threshold = page_cfg["rating_diff_threshold"]

        result = await fetch_list_page(client, category, inline_set, cursor)

        if result is None:
            logger.warning(f"[INCR ] id={task_id} fetch_list_page failed, cursor={cursor}")
            exit_reason = "ERROR"
            break

        if result == "BANNED":
            logger.warning(f"[INCR ] id={task_id} IP banned, cursor={cursor}")
            exit_reason = "BANNED"
            break

        items, next_cursor, _ = result
        if not items:
            exit_reason = "END"
            break

        if latest_gid is None:
            latest_gid = max(item.gid for item in items)
            cutoff_gid = max(0, latest_gid - cycle_gid_window)
            logger.info(
                f"[INCR ] id={task_id} cycle start category={category}"
                f" latest={latest_gid} cutoff={cutoff_gid}"
            )

        logger.debug(
            f"[INCR ] id={task_id} page cursor={cursor} items={len(items)} quota_left={quota}"
        )

        rows_to_upsert = []
        for item in items:
            if quota <= 0:
                break

            existing = get_gallery_meta(item.gid)
            if existing is None:
                detail = await fetch_detail(client, item.gid, item.token)
                quota -= 1
                if detail == "BANNED":
                    logger.warning(f"[INCR ] id={task_id} gid={item.gid} IP banned")
                    exit_reason = "BANNED"
                    quota = 0
                    break
                if detail:
                    rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))
                continue

            should_fetch, _ = should_refresh_from_list(existing, item, rating_threshold)
            if not should_fetch:
                continue

            detail = await fetch_detail(client, item.gid, item.token)
            quota -= 1
            if detail == "BANNED":
                logger.warning(f"[INCR ] id={task_id} gid={item.gid} IP banned")
                exit_reason = "BANNED"
                quota = 0
                break
            if detail:
                rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))

        if rows_to_upsert:
            db.upsert_galleries_bulk(rows_to_upsert)

        cursor = next_cursor
        progress_pct = calc_incremental_progress(latest_gid, cutoff_gid, cursor)
        logger.info(
            f"[INCR ] id={task_id} upserted={len(rows_to_upsert)}"
            f" quota_left={quota} progress={progress_pct:.1f}%"
        )

        if cursor is None:
            exit_reason = "END"
            break
        if cutoff_gid is not None and cursor <= cutoff_gid:
            exit_reason = "CUTOFF"
            break

    logger.info(f"[INCR ] id={task_id} exit_reason={exit_reason} round={round_num}")

    if exit_reason in ("END", "CUTOFF"):
        new_state = {
            "next_gid": None,
            "round": round_num + 1,
            "latest_gid": None,
            "cutoff_gid": None,
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
    state["cutoff_gid"] = cutoff_gid
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


async def run_task(client: AsyncSession, task_id: int):
    logger.info(f"[TASK ] start id={task_id}")

    try:
        while True:
            runtime = db.get_task_runtime(task_id)
            if not runtime:
                logger.info(f"[TASK ] id={task_id} deleted")
                return

            if runtime["desired_status"] != "running":
                if runtime["status"] not in ("completed", "error"):
                    db.update_task_runtime(task_id, status="stopped", touch_run_time=True)
                logger.info(f"[TASK ] id={task_id} stop requested")
                return

            # 校验 category 合法性
            category = runtime.get("category", "")
            if category not in VALID_CATEGORIES:
                msg = f"category '{category}' is not a valid ExHentai category. Valid: {sorted(VALID_CATEGORIES)}"
                logger.error(f"[TASK ] id={task_id} {msg}")
                db.update_task_runtime(task_id, status="error", error_message=msg, touch_run_time=True)
                db.set_task_desired_status(task_id, "stopped")
                return

            db.update_task_runtime(task_id, status="running", error_message="", touch_run_time=True)

            if runtime["type"] == "full":
                done = await run_full_once(client, task_id, runtime)
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
                item = db.claim_next_thumb_queue_item()
                if not item:
                    await asyncio.sleep(THUMB_IDLE_SLEEP)
                    continue

                item_id = item["id"]
                gid = item["gid"]
                thumb_url = item["thumb_url"]

                try:
                    await _thumb_rate_limiter.acquire()
                    resp = await client.get(
                        thumb_url,
                        timeout=15,
                        headers={"Referer": "https://exhentai.org/"},
                    )
                    if resp.status_code == 200:
                        (thumb_dir / str(gid)).write_bytes(resp.content)
                        db.mark_thumb_queue_done(item_id)
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


async def run_loop():
    logger.info("Starting DB-driven sync scheduler...")

    if any("YOUR_" in v for v in config.COOKIES.values()):
        logger.warning("Default cookies detected in .env, update EX_COOKIES.")

    global _rate_limiter, _thumb_rate_limiter
    _rate_limiter = GlobalRateLimiter(config.RATE_INTERVAL)
    _thumb_rate_limiter = GlobalRateLimiter(config.THUMB_RATE_INTERVAL)
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
                            running_tasks[task_id] = asyncio.create_task(
                                run_task(client, task_id),
                                name=f"sync-task-{task_id}",
                            )
                    else:
                        if task_obj and not task_obj.done():
                            task_obj.cancel()

                await asyncio.sleep(SCHEDULER_POLL_INTERVAL)
        finally:
            thumb_task.cancel()
            for task_obj in running_tasks.values():
                task_obj.cancel()
            await asyncio.gather(thumb_task, *running_tasks.values(), return_exceptions=True)
