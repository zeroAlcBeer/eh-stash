import asyncio
import logging
import random
import sys
from pathlib import Path
from curl_cffi.requests import AsyncSession
from psycopg2.extras import Json

import config
import db
from parser import parse_gallery_list, parse_detail, GalleryListItem

logger = logging.getLogger(__name__)

# ExHentai f_cats bitmask: each bit = a category to EXCLUDE
# To show ONLY one category, exclude all others (ALL_CATS - that_bit)
_ALL_CATS = 1023
_CATEGORY_BITS = {
    'Misc':       1,
    'Doujinshi':  2,
    'Manga':      4,
    'Artist CG':  8,
    'Game CG':    16,
    'Image Set':  32,
    'Cosplay':    64,
    'Asian Porn': 128,
    'Non-H':      256,
    'Western':    512,
}

CATEGORIES = ['Manga', 'Doujinshi', 'Cosplay']

async def validate_access(client: AsyncSession) -> bool:
    """Check if we can access the site without Sad Panda or Login errors."""
    url = config.EX_BASE_URL
    logger.info(f"Validating access to {url} ...")

    # Debug: print loaded cookies (mask values for security)
    cookie_keys = list(config.COOKIES.keys())
    cookie_debug = {k: v[:4] + "****" for k, v in config.COOKIES.items()}
    logger.info(f"[DEBUG] Loaded cookie keys: {cookie_keys}")
    logger.info(f"[DEBUG] Cookie preview: {cookie_debug}")

    try:
        resp = await client.get(url, timeout=30)

        logger.info(f"[DEBUG] Response status: {resp.status_code}")
        logger.info(f"[DEBUG] Response URL: {resp.url}")
        logger.info(f"[DEBUG] Response headers: {dict(resp.headers)}")
        logger.info(f"[DEBUG] HTML (first 800 chars):\n{resp.text[:800]}")

        if resp.status_code != 200:
            logger.error(f"Access check failed: HTTP {resp.status_code}")
            return False
            
        # Check for Sad Panda (image or text)
        if "panda.png" in resp.text or "Sad Panda" in resp.text:
            logger.critical("ACCESS DENIED: Sad Panda detected. Check your cookies (ipb_member_id, ipb_pass_hash, sk) in .env")
            return False
            
        # Check for Login requirement
        if "This page requires you to log on" in resp.text or "You must be logged in" in resp.text:
            logger.critical("ACCESS DENIED: Login required. Check your cookies in .env")
            return False

        # The real sad panda page has no navigation ("nb") and no "itg" gallery grid.
        # A logged-in page has id="nb" with real nav links (Front Page, Watched, Popular...).
        # Unauthenticated redirect has no "nb" nav and no gallery content.
        has_nav = 'id="nb"' in resp.text
        has_gallery = 'class="itg"' in resp.text or "itg glte" in resp.text or "itg gltc" in resp.text

        if not has_nav and not has_gallery:
            logger.critical("ACCESS DENIED: No navigation bar or gallery found. Cookies are invalid or expired (Sad Panda).")
            logger.critical(f"HTML Preview: {resp.text[:500]}...")
            return False

        logger.info("Access check passed. Starting loop.")
        return True
    except Exception as e:
        logger.error(f"Access check failed with exception: {e}")
        return False

async def fetch_list_page(client: AsyncSession, category: str, next_gid: int | None = None):
    """
    获取列表页。ExHentai 使用游标分页：
      第一页: ?f_cats=XXX
      后续页: ?f_cats=XXX&next=<gid>
    返回 (items, next_cursor) 或 None（出错时）
    """
    fcats = _ALL_CATS - _CATEGORY_BITS.get(category, 0)
    url = f"{config.EX_BASE_URL}/?f_cats={fcats}&inline_set={config.CALLBACK_INLINE_SET}"
    if next_gid is not None:
        url += f"&next={next_gid}"
    cursor_label = f"next={next_gid}" if next_gid else "first"
    logger.info(f"[LIST ] {category:<10} {cursor_label:<16} {url}")
    try:
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"List page status {resp.status_code}")
            return None
        
        if "panda.png" in resp.text or "Sad Panda" in resp.text:
            logger.error("Sad Panda detected! Your cookies are invalid or IP is banned.")
            return None

        if "This page requires you to log on" in resp.text:
             logger.error("Login required! Please check your cookies.")
             return None

        items, next_cursor = parse_gallery_list(resp.text)
        if not items:
            logger.warning(f"No items found in list page. HTML preview: {resp.text[:500]}...")
            
        return items, next_cursor
    except Exception as e:
        logger.error(f"Error fetching list page: {e}")
        return None

async def fetch_detail(client: AsyncSession, gid: int, token: str):
    url = f"{config.EX_BASE_URL}/g/{gid}/{token}/"
    # logger.info(f"[DETAIL] gid={gid} {url}") 
    # Log handled in loop for consistency
    try:
        resp = await client.get(url, timeout=30)
        if resp.status_code != 200:
            logger.warning(f"Detail page status {resp.status_code}")
            return None
        return parse_detail(resp.text)
    except Exception as e:
        logger.error(f"Error fetching detail: {e}")
        return None

def _default_state(job_name: str) -> dict:
    if job_name.startswith("scraper-"):
        return {"next_gid": None, "round": 0, "done": False}
    return {"next_gid": None, "round": 0, "latest_gid": None, "cutoff_gid": None}


def get_state(job_name):
    default_state = _default_state(job_name)
    with db.get_cursor() as (cur, conn):
        cur.execute("SELECT state FROM schedule_state WHERE job_name = %s", (job_name,))
        row = cur.fetchone()
        state = row[0] if (row and row[0]) else dict(default_state)
        dirty = not (row and row[0])

        # Migrate old state formats and fill new keys.
        if "round" not in state:
            state["round"] = 0
            dirty = True
        if "current_page" in state:
            del state["current_page"]
            state.setdefault("next_gid", None)
            dirty = True
        if "next_gid" not in state:
            state["next_gid"] = None
            dirty = True
        if job_name.startswith("scraper-") and "done" not in state:
            state["done"] = False
            dirty = True
        if job_name.startswith("callback-"):
            if "latest_gid" not in state:
                state["latest_gid"] = None
                dirty = True
            if "cutoff_gid" not in state:
                state["cutoff_gid"] = None
                dirty = True

        if dirty:
            # Persist migrated shape so later reads are consistent.
            cur.execute(
                "INSERT INTO schedule_state (job_name, state, last_run_at) VALUES (%s, %s, NOW()) "
                "ON CONFLICT (job_name) DO UPDATE SET state = EXCLUDED.state",
                (job_name, Json(state)),
            )
        return state


def save_state(job_name, state):
    with db.get_cursor() as (cur, conn):
        cur.execute(
            "UPDATE schedule_state SET state = %s, last_run_at = NOW() WHERE job_name = %s",
            (Json(state), job_name)
        )


def ensure_job_row(job_name):
    """旧 DB 可能缺行；按默认状态补齐。"""
    with db.get_cursor() as (cur, conn):
        cur.execute(
            "INSERT INTO schedule_state (job_name, state) VALUES (%s, %s) ON CONFLICT DO NOTHING",
            (job_name, Json(_default_state(job_name)))
        )


def get_gallery_meta(gid):
    with db.get_cursor() as (cur, conn):
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


def should_refresh_from_list(existing: dict, item: GalleryListItem) -> tuple[bool, list[str]]:
    reasons: list[str] = []

    if item.visible_tag_count != existing["tag_count"]:
        reasons.append(f"tags:{existing['tag_count']}->{item.visible_tag_count}")

    detail_bucket = bucket_rating(existing["rating"])
    list_bucket = bucket_rating(item.rating_est)

    if detail_bucket is None and list_bucket is not None:
        reasons.append(f"rating:none->{list_bucket:.1f}")
    elif detail_bucket is not None and list_bucket is not None:
        diff = abs(detail_bucket - list_bucket)
        if diff >= config.CALLBACK_RATING_DIFF_THRESHOLD:
            reasons.append(f"rating:{detail_bucket:.1f}->{list_bucket:.1f}")

    return bool(reasons), reasons


def build_jobs_for_cycle() -> list[str]:
    jobs = [f"callback-{c.lower()}" for c in CATEGORIES]
    for category in CATEGORIES:
        scraper_job = f"scraper-{category.lower()}"
        scraper_state = get_state(scraper_job)
        if not scraper_state.get("done", False):
            jobs.append(scraper_job)
    random.shuffle(jobs)
    return jobs

def build_upsert_row(gid, token, detail):
    return (
        gid,
        token,
        detail.get('category'),
        detail.get('title'),
        detail.get('title_jpn'),
        detail.get('uploader'),
        detail.get('posted'),
        detail.get('language'),
        detail.get('pages'),
        detail.get('rating'),
        detail.get('fav_count'),
        detail.get('comment_count', 0),
        detail.get('thumb'),
        Json(detail.get('tags', {})),
    )

async def run_loop():
    logger.info("Starting loop (scraper backfill + callback incremental)...")

    if any("YOUR_" in v for v in config.COOKIES.values()):
        logger.warning("Default cookies detected in .env! Please update EX_COOKIES with real values.")

    # 兼容旧 DB：确保 scraper-/callback- 行都存在。
    for cat in CATEGORIES:
        ensure_job_row(f"scraper-{cat.lower()}")
        ensure_job_row(f"callback-{cat.lower()}")

    async with AsyncSession(
        headers=config.HEADERS,
        cookies=config.COOKIES,
        allow_redirects=True,
        timeout=30,
        impersonate="chrome",
        verify=False,
    ) as client:

        if not await validate_access(client):
            logger.critical("Startup validation failed. Exiting.")
            sys.exit(1)

        # thumb 下载器独立运行，使用自己的 Session，不参与 round-robin
        asyncio.create_task(run_thumb_loop())

        while True:
            jobs = build_jobs_for_cycle()
            if not jobs:
                logger.warning("No jobs available in current cycle, sleeping 5s.")
                await asyncio.sleep(5)
                continue

            for job_name in jobs:
                is_callback = job_name.startswith("callback-")
                category = job_name.split("-", 1)[1].capitalize()  # manga→Manga

                if is_callback:
                    await _run_callback_job(client, job_name, category)
                else:
                    await _run_scraper_job(client, job_name, category)


# ---------------------------------------------------------------------------
# scraper- 全量慢轨：无条件抓取每一条，不做活跃检测
# ---------------------------------------------------------------------------
async def _run_scraper_job(client: AsyncSession, job_name: str, category: str):
    state = get_state(job_name)
    if state.get("done", False):
        return

    next_gid = state.get("next_gid")
    round_num = state.get("round", 0)

    result = await fetch_list_page(client, category, next_gid)
    await asyncio.sleep(config.RATE_INTERVAL)

    if result is None:
        return  # 网络错误，不推进游标

    items, next_cursor = result

    if not items:
        # 无结果视为分类回填已结束，标记 done。
        next_round = round_num + 1
        logger.info(f"[SCRAPER] {category} 全量回填结束，round {round_num}→{next_round}，停止该轨。")
        save_state(job_name, {"next_gid": None, "round": next_round, "done": True})
        return

    rows_to_upsert = []

    for item in items:
        existing = get_gallery_meta(item.gid)
        detail = await fetch_detail(client, item.gid, item.token)
        await asyncio.sleep(config.RATE_INTERVAL)
        if detail:
            action = "+INSERT" if not existing else "UPDATE "
            logger.info(f"[SCRAPER] {category:<10} gid={item.gid} {action} fav={detail.get('fav_count')}")
            rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))
        else:
            logger.warning(f"[SCRAPER] {category:<10} gid={item.gid} detail fetch failed")

    if rows_to_upsert:
        db.upsert_galleries_bulk(rows_to_upsert)

    save_state(job_name, {"next_gid": next_cursor, "round": round_num, "done": False})
    if next_cursor is None:
        # 到达最后一页后不再回到第一页，直接停止 scraper- 轨。
        next_round = round_num + 1
        logger.info(f"[SCRAPER] {category} 末页，round {round_num}→{next_round}，标记 done。")
        save_state(job_name, {"next_gid": None, "round": next_round, "done": True})


# ---------------------------------------------------------------------------
# callback- 增量快轨：按游标循环追踪最新窗口
#   - 每 turn 持有 CALLBACK_DETAIL_QUOTA 个 detail 请求额度
#   - 一整轮周期：从最新页追到 cutoff(latest_gid - CALLBACK_GID_WINDOW)
#   - existing is None：立即抓 detail + 入库
#   - existing != None：仅当 list 粗粒度信号变化时抓 detail 更新
# ---------------------------------------------------------------------------
async def _run_callback_job(client: AsyncSession, job_name: str, category: str):
    state = get_state(job_name)
    cursor = state.get("next_gid")       # 当前页游标
    round_num = state.get("round", 0)
    latest_gid = state.get("latest_gid")
    cutoff_gid = state.get("cutoff_gid")
    if cursor is not None and (latest_gid is None or cutoff_gid is None):
        # Legacy/incomplete state: restart from first page to re-anchor latest/cutoff.
        logger.info(f"[CALLBK] {category:<10} state incomplete, reset cursor to first page.")
        cursor = None
        latest_gid = None
        cutoff_gid = None

    quota = config.CALLBACK_DETAIL_QUOTA  # 本 turn 剩余 detail 额度
    pages_visited = 0
    exit_reason = None

    while quota > 0:
        result = await fetch_list_page(client, category, cursor)
        await asyncio.sleep(config.RATE_INTERVAL)

        if result is None:
            break  # 网络错误，保存当前位置

        items, next_cursor = result
        pages_visited += 1

        if not items:
            exit_reason = "END"
            break

        if latest_gid is None:
            latest_gid = max(item.gid for item in items)
            cutoff_gid = max(0, latest_gid - config.CALLBACK_GID_WINDOW)
            logger.info(
                f"[CALLBK] {category:<10} start-cycle latest={latest_gid} cutoff={cutoff_gid} "
                f"window={config.CALLBACK_GID_WINDOW}"
            )

        rows_to_upsert = []
        inserted_count = 0
        updated_count = 0

        for item in items:
            if quota <= 0:
                break

            existing = get_gallery_meta(item.gid)
            if existing is None:
                detail = await fetch_detail(client, item.gid, item.token)
                await asyncio.sleep(config.RATE_INTERVAL)
                quota -= 1
                if detail:
                    rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))
                    inserted_count += 1
                    logger.info(
                        f"[CALLBK] {category:<10} gid={item.gid} +INSERT NEW (quota={quota})"
                    )
                else:
                    logger.warning(f"[CALLBK] {category:<10} gid={item.gid} detail fetch failed on NEW")
                continue

            should_fetch, reasons = should_refresh_from_list(existing, item)

            if not should_fetch:
                logger.debug(f"[CALLBK] {category:<10} gid={item.gid} SKIP coarse-match")
                continue

            detail = await fetch_detail(client, item.gid, item.token)
            await asyncio.sleep(config.RATE_INTERVAL)
            quota -= 1

            if detail:
                rows_to_upsert.append(build_upsert_row(item.gid, item.token, detail))
                updated_count += 1
                logger.info(
                    f"[CALLBK] {category:<10} gid={item.gid} UPDATE coarse={','.join(reasons)} "
                    f"(quota={quota})"
                )
            else:
                logger.warning(f"[CALLBK] {category:<10} gid={item.gid} detail fetch failed")

        if rows_to_upsert:
            db.upsert_galleries_bulk(rows_to_upsert)
        logger.info(
            f"[CALLBK] {category:<10} page_done rows={len(rows_to_upsert)} "
            f"(inserted={inserted_count}, updated={updated_count}, quota_left={quota})"
        )

        # 推进游标到下一页
        cursor = next_cursor

        # 检查退出条件
        if cursor is None:
            exit_reason = "END"
            break
        if cutoff_gid is not None and cursor <= cutoff_gid:
            exit_reason = "CUTOFF"
            break
        if quota <= 0:
            exit_reason = "QUOTA"
            break

    # 决定保存状态
    if exit_reason in ("CUTOFF", "END"):
        logger.info(
            f"[CALLBK] {category} {exit_reason} → 完成一轮并重置游标 "
            f"(pages={pages_visited}, quota_left={quota}, latest={latest_gid}, cutoff={cutoff_gid})"
        )
        save_state(
            job_name,
            {"next_gid": None, "round": round_num + 1, "latest_gid": None, "cutoff_gid": None},
        )
    else:
        logger.info(
            f"[CALLBK] {category} {exit_reason or 'ERROR'} → 保存位置 cursor={cursor} "
            f"(pages={pages_visited}, quota_left={quota}, latest={latest_gid}, cutoff={cutoff_gid})"
        )
        save_state(
            job_name,
            {
                "next_gid": cursor,
                "round": round_num,
                "latest_gid": latest_gid,
                "cutoff_gid": cutoff_gid,
            },
        )


# ---------------------------------------------------------------------------
# thumb 下载器：独立运行，不参与 round-robin
#   - 每轮 diff(DB, 本地文件)，下载差集
#   - 差集为空时 sleep 30s；有差集时持续追赶，不额外 sleep
# ---------------------------------------------------------------------------
async def run_thumb_loop():
    thumb_dir = Path(config.THUMB_DIR)
    thumb_dir.mkdir(parents=True, exist_ok=True)
    logger.info(f"[THUMB ] 启动，目录={thumb_dir}")

    async with AsyncSession(
        headers=config.HEADERS,
        cookies=config.COOKIES,
        allow_redirects=True,
        timeout=15,
        impersonate="chrome",
        verify=False,
    ) as client:
        while True:
            try:
                all_thumbs = db.get_all_thumb_urls()          # {gid: url}
                local_gids = {int(p.stem) for p in thumb_dir.iterdir() if p.stem.isdigit()}
                missing = {gid: url for gid, url in all_thumbs.items() if gid not in local_gids}

                if not missing:
                    logger.debug(f"[THUMB ] 已全部同步 ({len(local_gids)} 张)，sleep 30s")
                    await asyncio.sleep(30)
                    continue

                logger.info(f"[THUMB ] 差集 {len(missing)} 张，开始下载")

                for gid, url in missing.items():
                    try:
                        resp = await client.get(
                            url,
                            timeout=15,
                            headers={"Referer": "https://exhentai.org/"},
                        )
                        if resp.status_code == 200:
                            dest = thumb_dir / str(gid)
                            dest.write_bytes(resp.content)
                            logger.debug(f"[THUMB ] gid={gid} OK ({len(resp.content)} B)")
                        else:
                            logger.warning(f"[THUMB ] gid={gid} HTTP {resp.status_code}")
                    except Exception as e:
                        logger.warning(f"[THUMB ] gid={gid} 下载失败: {e}")

                    await asyncio.sleep(config.THUMB_RATE_INTERVAL)

            except Exception as e:
                logger.error(f"[THUMB ] 循环异常: {e}")
                await asyncio.sleep(10)
