# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "curl-cffi",
#   "beautifulsoup4",
#   "lxml",
#   "python-dotenv",
# ]
# ///
"""
Validate dm_e list parsing by comparing list-page signals against detail-page truth.

Workflow:
1) Fetch exactly one list page.
2) For every list item, fetch detail page one by one.
3) Print per-item diff for title/rating/tags (list vs detail).
"""

from __future__ import annotations

import argparse
import asyncio
import os
import re
from dataclasses import dataclass
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from bs4 import BeautifulSoup, Tag
from curl_cffi.requests import AsyncSession
from dotenv import load_dotenv

GID_TOKEN_RE = re.compile(r"/g/(\d+)/([a-f0-9]+)/")
RATING_RE = re.compile(r"([0-5](?:\.\d+)?)")
TOTAL_COUNT_RE = re.compile(r"Found\s+(?:about\s+)?([\d,]+)\s+results")
TAG_CLASS_RE = re.compile(r"^gt")
BG_POS_RE = re.compile(r"background-position\s*:\s*(-?\d+)px\s+(-?\d+)px")
SPACE_RE = re.compile(r"\s+")

DEFAULT_QUERY_TEMPLATE = "/?f_search=language%3Achinese+language%3Atranslated"
DEFAULT_INLINE_SET = "dm_e"
INLINE_SET_SHORT = {"m", "p", "l", "e", "t"}


@dataclass(frozen=True)
class ListSignal:
    gid: int
    token: str
    title: str
    rating_sig: str
    rating_est: float | None
    tags: tuple[str, ...]


@dataclass(frozen=True)
class DetailSignal:
    title: str
    rating: float | None
    tags: tuple[str, ...]


def normalize_text(value: str) -> str:
    return SPACE_RE.sub(" ", value or "").strip()


def bucket_rating(value: float | None) -> float | None:
    if value is None:
        return None
    return round(value * 2.0) / 2.0


def parse_cookie_string(raw: str) -> dict[str, str]:
    cookies: dict[str, str] = {}
    if not raw:
        return cookies
    for pair in raw.split(";"):
        if "=" not in pair:
            continue
        key, value = pair.split("=", 1)
        key = key.strip()
        value = value.strip()
        if key and value:
            cookies[key] = value
    return cookies


def extract_title(glname: Tag) -> str:
    node: Tag | str = glname
    while isinstance(node, Tag):
        children = [c for c in node.children if isinstance(c, Tag)]
        if not children:
            break
        node = children[0]
    if isinstance(node, Tag):
        return normalize_text(node.get_text(" ", strip=True))
    return normalize_text(str(node))


def extract_rating_signal(element: Tag) -> tuple[str, float | None]:
    for ir in element.find_all(class_=re.compile(r"\bir")):
        style = normalize_text(ir.get("style", ""))
        matched = BG_POS_RE.search(style)
        if matched:
            x, y = int(matched.group(1)), int(matched.group(2))
            if y == -1:
                value = max(0.0, min(5.0, 5.0 - abs(x) / 16.0))
                return f"sprite:x={x},y={y}", value
            if y == -21:
                value = max(0.0, min(5.0, 4.5 - abs(x) / 16.0))
                return f"sprite:x={x},y={y}", value

        title = normalize_text(ir.get("title", ""))
        if title:
            matched = RATING_RE.search(title)
            if matched:
                value = float(matched.group(1))
                return f"title:{value:.2f}", value

    for klass in ("gl4e", "gl4t", "gl5t", "gl5m", "gl5c"):
        node = element.find(class_=klass)
        if not node:
            continue
        text = normalize_text(node.get_text(" ", strip=True))
        matched = RATING_RE.search(text)
        if matched:
            value = float(matched.group(1))
            return f"text:{value:.2f}", value

    return "", None


def extract_visible_tags(element: Tag) -> tuple[str, ...]:
    tags: set[str] = set()

    for node in element.find_all(True):
        classes = node.get("class") or []
        if not any(TAG_CLASS_RE.match(c) for c in classes):
            continue
        text = normalize_text(node.get_text(" ", strip=True)).lower()
        if not text:
            continue
        if len(text) > 80:
            continue
        tags.add(text)

    if not tags:
        for a in element.find_all("a", href=True):
            href = a.get("href", "")
            if "f_search=" not in href:
                continue
            text = normalize_text(a.get_text(" ", strip=True)).lower()
            if not text or len(text) > 80:
                continue
            if text in {"archive download", "torrent download"}:
                continue
            tags.add(text)

    return tuple(sorted(tags))


def parse_list_page(html: str) -> tuple[list[ListSignal], int | None]:
    soup = BeautifulSoup(html, "lxml")
    itg = soup.find(class_="itg")
    if not itg:
        return [], None

    total_count: int | None = None
    matched = TOTAL_COUNT_RE.search(html)
    if matched:
        total_count = int(matched.group(1).replace(",", ""))

    items: list[ListSignal] = []
    for element in itg.find_all(recursive=False):
        glname = element.find(class_="glname")
        if not glname:
            continue

        link = glname.find("a")
        if link is None:
            parent = glname.parent
            if parent and parent.name == "a":
                link = parent
        if link is None:
            continue

        href = link.get("href", "")
        matched = GID_TOKEN_RE.search(href)
        if not matched:
            continue

        gid = int(matched.group(1))
        token = matched.group(2)
        title = extract_title(glname)
        rating_sig, rating_est = extract_rating_signal(element)
        tags = extract_visible_tags(element)
        items.append(
            ListSignal(
                gid=gid,
                token=token,
                title=title,
                rating_sig=rating_sig,
                rating_est=rating_est,
                tags=tags,
            )
        )

    return items, total_count


def parse_detail_page(html: str) -> DetailSignal:
    soup = BeautifulSoup(html, "lxml")
    gm = soup.find(class_="gm")
    if not gm:
        return DetailSignal(title="", rating=None, tags=())

    gn = gm.find(id="gn")
    title = normalize_text(gn.get_text(" ", strip=True)) if gn else ""

    rating: float | None = None
    rating_label = gm.find(id="rating_label")
    if rating_label:
        text = normalize_text(rating_label.get_text(" ", strip=True))
        if "Not Yet Rated" not in text:
            matched = RATING_RE.search(text)
            if matched:
                rating = float(matched.group(1))

    detail_tags: set[str] = set()
    taglist = soup.find(id="taglist")
    if taglist:
        for tr in taglist.find_all("tr"):
            tds = tr.find_all("td")
            if len(tds) < 2:
                continue
            for div in tds[1].find_all("div"):
                a = div.find("a")
                if not a:
                    continue
                text = normalize_text(a.get_text(" ", strip=True)).lower()
                if text:
                    detail_tags.add(text)

    return DetailSignal(
        title=title,
        rating=rating,
        tags=tuple(sorted(detail_tags)),
    )


def normalize_inline_set(inline_set: str) -> str:
    value = (inline_set or "").strip().lower()
    if not value:
        return DEFAULT_INLINE_SET
    if value in INLINE_SET_SHORT:
        return f"dm_{value}"
    return value


def apply_inline_set(url: str, inline_set: str) -> str:
    if (inline_set or "").strip().lower() in {"keep", "off", "none"}:
        return url

    mode = normalize_inline_set(inline_set)
    split = urlsplit(url)
    query = dict(parse_qsl(split.query, keep_blank_values=True))
    query["inline_set"] = mode
    return urlunsplit(
        (split.scheme, split.netloc, split.path, urlencode(query, doseq=True), split.fragment)
    )


def strip_query_keys(url: str, keys: set[str]) -> str:
    split = urlsplit(url)
    query = dict(parse_qsl(split.query, keep_blank_values=True))
    for key in keys:
        query.pop(key, None)
    return urlunsplit(
        (split.scheme, split.netloc, split.path, urlencode(query, doseq=True), split.fragment)
    )


def build_start_url(base_url: str, query_template: str, inline_set: str) -> str:
    base = base_url.rstrip("/")
    query = query_template
    if "{page}" in query:
        query = query.replace("{page}", "0")
    if not query.startswith("/"):
        query = "/" + query
    url = base + query
    url = strip_query_keys(url, {"page", "next"})
    return apply_inline_set(url, inline_set)


async def fetch_first_list_page(
    client: AsyncSession,
    url: str,
) -> tuple[list[ListSignal], int | None]:
    print(f"[LIST ] GET {url}")
    resp = await client.get(url, timeout=30)
    resp.raise_for_status()
    items, total_count = parse_list_page(resp.text)
    with_rating = sum(1 for s in items if s.rating_est is not None)
    with_tags = sum(1 for s in items if s.tags)
    print(
        f"[LIST ] items={len(items)} total_count={total_count if total_count is not None else '-'} "
        f"rating_detected={with_rating} tags_detected={with_tags}"
    )
    return items, total_count


async def fetch_detail_signal(
    client: AsyncSession,
    base_url: str,
    gid: int,
    token: str,
) -> DetailSignal | None:
    url = f"{base_url.rstrip('/')}/g/{gid}/{token}/"
    try:
        resp = await client.get(url, timeout=30)
        resp.raise_for_status()
    except Exception as exc:
        print(f"[WARN ] gid={gid} detail fetch failed: {exc}")
        return None
    return parse_detail_page(resp.text)


def format_rating(value: float | None) -> str:
    if value is None:
        return "-"
    return f"{value:.1f}"


def format_tags(tags: tuple[str, ...], limit: int = 8) -> str:
    if not tags:
        return "-"
    if len(tags) <= limit:
        return ", ".join(tags)
    return ", ".join(tags[:limit]) + f", ... (+{len(tags) - limit})"


def print_diff(
    idx: int,
    item: ListSignal,
    detail: DetailSignal,
) -> tuple[bool, bool, bool]:
    list_rating = bucket_rating(item.rating_est)
    detail_rating = bucket_rating(detail.rating)
    rating_diff = list_rating != detail_rating

    list_tags = set(item.tags)
    detail_tags = set(detail.tags)
    missing = sorted(list_tags - detail_tags)
    extra = sorted(detail_tags - list_tags)
    tags_diff = bool(missing)

    title_diff = bool(detail.title and normalize_text(item.title) != normalize_text(detail.title))

    print(f"\n[{idx:02d}] gid={item.gid}")
    print(f"  title(list):   {item.title or '-'}")
    print(f"  title(detail): {detail.title or '-'}")
    print(
        f"  rating: list={format_rating(list_rating)} ({item.rating_sig or '-'}) "
        f"detail={format_rating(detail_rating)} -> {'DIFF' if rating_diff else 'OK'}"
    )
    print(
        f"  tags:   list={len(list_tags)} detail={len(detail_tags)} "
        f"missing={len(missing)} extra={len(extra)} -> {'DIFF' if tags_diff else 'OK'}"
    )
    if missing:
        print(f"    missing_in_detail: {format_tags(tuple(missing))}")
    if extra:
        print(f"    extra_in_detail:   {format_tags(tuple(extra))}")

    return rating_diff, tags_diff, title_diff


async def main() -> None:
    load_dotenv()

    parser = argparse.ArgumentParser(description="Validate dm_e list parsing via list vs detail diff.")
    parser.add_argument(
        "--base-url",
        default=os.getenv("EX_BASE_URL", "https://e-hentai.org"),
        help="Base site URL, e.g. https://exhentai.org or https://e-hentai.org",
    )
    parser.add_argument(
        "--query-template",
        default=DEFAULT_QUERY_TEMPLATE,
        help="One list page to fetch (next/page params are ignored).",
    )
    parser.add_argument(
        "--inline-set",
        default=DEFAULT_INLINE_SET,
        help="List display mode. Recommended: dm_e (or short: e).",
    )
    parser.add_argument(
        "--detail-interval",
        type=float,
        default=1.0,
        help="Delay seconds between detail requests.",
    )
    parser.add_argument(
        "--limit",
        type=int,
        default=0,
        help="Only validate first N items from list page (0 means all).",
    )
    args = parser.parse_args()

    cookies = parse_cookie_string(os.getenv("EX_COOKIES", ""))
    if cookies:
        print(f"[INFO ] cookies loaded: keys={sorted(cookies.keys())}")
    else:
        print("[INFO ] no cookies loaded from EX_COOKIES.")

    resolved_inline_set = normalize_inline_set(args.inline_set)
    if args.inline_set.strip().lower() in {"keep", "off", "none"}:
        print("[INFO ] inline_set mode: keep server default")
    else:
        print(f"[INFO ] inline_set mode: {resolved_inline_set}")

    headers = {
        "User-Agent": (
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
            "AppleWebKit/537.36 (KHTML, like Gecko) "
            "Chrome/124.0.0.0 Safari/537.36"
        ),
        "Accept-Language": "en-US,en;q=0.9",
        "Referer": args.base_url,
    }

    async with AsyncSession(
        headers=headers,
        cookies=cookies,
        allow_redirects=True,
        timeout=30,
        impersonate="chrome",
        verify=False,
    ) as client:
        start_url = build_start_url(args.base_url, args.query_template, args.inline_set)
        items, _ = await fetch_first_list_page(client, start_url)

        if not items:
            print("[WARN ] no items parsed from list page.")
            return

        if args.limit > 0:
            items = items[: args.limit]
            print(f"[INFO ] limiting to first {len(items)} items")

        rating_diff_count = 0
        tags_diff_count = 0
        title_diff_count = 0
        detail_fail_count = 0

        print(f"\n=== validating {len(items)} items ===")
        for idx, item in enumerate(items, start=1):
            detail = await fetch_detail_signal(client, args.base_url, item.gid, item.token)
            if detail is None:
                detail_fail_count += 1
                continue

            rating_diff, tags_diff, title_diff = print_diff(idx, item, detail)
            rating_diff_count += int(rating_diff)
            tags_diff_count += int(tags_diff)
            title_diff_count += int(title_diff)

            if idx < len(items) and args.detail_interval > 0:
                await asyncio.sleep(args.detail_interval)

    print("\n=== summary ===")
    print(f"items_total        = {len(items)}")
    print(f"detail_fetch_fail  = {detail_fail_count}")
    print(f"rating_diff_count  = {rating_diff_count}")
    print(f"tags_diff_count    = {tags_diff_count}")
    print(f"title_diff_count   = {title_diff_count}")


if __name__ == "__main__":
    asyncio.run(main())
