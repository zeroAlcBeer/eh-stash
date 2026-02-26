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
List-signal trigger probe for coarse update detection.

Workflow:
1) Fetch N list pages and keep only in-memory signals per gid.
2) Wait M seconds.
3) Fetch the same N pages again.
4) Compare rating/tag signals and report trigger ratio:
      triggered / comparable

This is intentionally "list-only": no detail requests and no DB writes.
"""

from __future__ import annotations

import argparse
import asyncio
import os
import re
from dataclasses import dataclass
from typing import Iterable
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from bs4 import BeautifulSoup, Tag
from curl_cffi.requests import AsyncSession
from dotenv import load_dotenv

GID_TOKEN_RE = re.compile(r"/g/(\d+)/([a-f0-9]+)/")
NEXT_CURSOR_RE = re.compile(r"[?&]next=(\d+)")
RATING_RE = re.compile(r"([0-5](?:\.\d+)?)")
TAG_CLASS_RE = re.compile(r"^gt")
SPACE_RE = re.compile(r"\s+")

DEFAULT_QUERY_TEMPLATE = "/?f_search=language%3Achinese+language%3Atranslated"
DEFAULT_INLINE_SET = "dm_l"
INLINE_SET_SHORT = {"m", "p", "l", "e", "t"}


@dataclass(frozen=True)
class ListSignal:
    gid: int
    token: str
    title: str
    rating_sig: str
    tags: tuple[str, ...]

    @property
    def tags_sig(self) -> str:
        return "|".join(self.tags)


def normalize_text(value: str) -> str:
    return SPACE_RE.sub(" ", value or "").strip()


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


def extract_rating_sig(element: Tag) -> str:
    # Most list styles expose a star widget .ir* with either title or style signals.
    ir_node = element.find(class_=re.compile(r"\bir"))
    if ir_node:
        title = normalize_text(ir_node.get("title", ""))
        if title:
            match = RATING_RE.search(title)
            if match:
                return f"avg:{float(match.group(1)):.2f}"
            return f"title:{title}"
        style = normalize_text(ir_node.get("style", ""))
        if style:
            return f"style:{style.replace(' ', '')}"
        classes = [c for c in (ir_node.get("class") or []) if c]
        if classes:
            return f"class:{','.join(sorted(classes))}"

    # Fallback: some styles include textual rating in summary cells.
    for klass in ("gl4e", "gl4t", "gl5t", "gl5m", "gl5c"):
        node = element.find(class_=klass)
        if not node:
            continue
        text = normalize_text(node.get_text(" ", strip=True))
        match = RATING_RE.search(text)
        if match:
            return f"text:{float(match.group(1)):.2f}"
    return ""


def extract_visible_tags(element: Tag) -> tuple[str, ...]:
    tags: set[str] = set()

    # Preferred: explicit tag-like classes.
    for node in element.find_all(True):
        classes = node.get("class") or []
        if not any(TAG_CLASS_RE.match(c) for c in classes):
            continue
        text = normalize_text(node.get_text(" ", strip=True))
        if not text:
            continue
        if len(text) > 80:
            continue
        tags.add(text.lower())

    # Fallback: visible tag links in list page usually include f_search.
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


def parse_next_cursor(soup: BeautifulSoup) -> int | None:
    dnext = soup.find(id="dnext")
    if not dnext:
        return None
    href = dnext.get("href", "")
    match = NEXT_CURSOR_RE.search(href)
    if not match:
        return None
    return int(match.group(1))


def parse_list_page(html: str) -> tuple[list[ListSignal], int | None]:
    soup = BeautifulSoup(html, "lxml")
    itg = soup.find(class_="itg")
    if not itg:
        return [], None

    signals: list[ListSignal] = []
    for element in itg.find_all(recursive=False):
        glname = element.find(class_="glname")
        if not glname:
            continue

        a = glname.find("a")
        if a is None:
            parent = glname.parent
            if parent and parent.name == "a":
                a = parent
        if a is None:
            continue

        href = a.get("href", "")
        match = GID_TOKEN_RE.search(href)
        if not match:
            continue

        gid = int(match.group(1))
        token = match.group(2)
        title = extract_title(glname)
        rating_sig = extract_rating_sig(element)
        tags = extract_visible_tags(element)

        signals.append(
            ListSignal(
                gid=gid,
                token=token,
                title=title,
                rating_sig=rating_sig,
                tags=tags,
            )
        )
    return signals, parse_next_cursor(soup)


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
    url = strip_query_keys(url, {"page"})
    return apply_inline_set(url, inline_set)


def build_next_url(current_url: str, next_cursor: int | None) -> str | None:
    if next_cursor is None:
        return None
    split = urlsplit(current_url)
    query = dict(parse_qsl(split.query, keep_blank_values=True))
    query.pop("page", None)
    query["next"] = str(next_cursor)
    return urlunsplit(
        (split.scheme, split.netloc, split.path, urlencode(query, doseq=True), split.fragment)
    )


async def fetch_page_signals(
    client: AsyncSession,
    url: str,
    step: int,
) -> tuple[list[ListSignal], int | None]:
    print(f"[LIST ] step={step:2d}  {url}")
    resp = await client.get(url, timeout=30)
    resp.raise_for_status()
    signals, next_cursor = parse_list_page(resp.text)

    with_rating = sum(1 for s in signals if s.rating_sig)
    with_tags = sum(1 for s in signals if s.tags)
    print(
        f"        -> items={len(signals)} rating_detected={with_rating} "
        f"tags_detected={with_tags} next={next_cursor if next_cursor is not None else '-'}"
    )
    return signals, next_cursor


async def collect_snapshot(
    client: AsyncSession,
    *,
    name: str,
    pages: int,
    base_url: str,
    query_template: str,
    inline_set: str,
    list_interval: float,
) -> dict[int, ListSignal]:
    print(f"\n=== {name}: collecting {pages} pages ===")
    snapshot: dict[int, ListSignal] = {}
    current_url = build_start_url(base_url, query_template, inline_set)

    for step in range(pages):
        try:
            signals, next_cursor = await fetch_page_signals(client, current_url, step)
        except Exception as exc:
            print(f"[WARN ] step={step} fetch failed: {exc}")
            continue

        for signal in signals:
            # Keep first occurrence if gid appears multiple times in the sample window.
            snapshot.setdefault(signal.gid, signal)

        if step < pages - 1:
            current_url = build_next_url(current_url, next_cursor)
            if current_url is None:
                print(f"[INFO ] {name}: no dnext cursor at step={step}, stop early.")
                break
            if list_interval > 0:
                await asyncio.sleep(list_interval)

    print(f"[INFO ] {name}: unique_gids={len(snapshot)}")
    return snapshot


def wait_steps(seconds: int, step: int = 30) -> Iterable[int]:
    remaining = seconds
    while remaining > 0:
        chunk = step if remaining >= step else remaining
        yield chunk
        remaining -= chunk


async def controlled_wait(seconds: int) -> None:
    if seconds <= 0:
        return
    print(f"\n=== waiting {seconds} seconds before second snapshot ===")
    waited = 0
    for chunk in wait_steps(seconds):
        await asyncio.sleep(chunk)
        waited += chunk
        print(f"[WAIT ] {waited}/{seconds} sec")


def compare_snapshots(
    first: dict[int, ListSignal],
    second: dict[int, ListSignal],
) -> tuple[list[tuple[int, ListSignal, ListSignal, list[str]]], int, int, int]:
    common_gids = sorted(set(first) & set(second))
    changed: list[tuple[int, ListSignal, ListSignal, list[str]]] = []

    for gid in common_gids:
        before = first[gid]
        after = second[gid]
        reasons: list[str] = []
        if before.rating_sig != after.rating_sig:
            reasons.append("rating")
        if before.tags_sig != after.tags_sig:
            reasons.append("tags")
        if reasons:
            changed.append((gid, before, after, reasons))

    new_in_second = len(set(second) - set(first))
    missing_in_second = len(set(first) - set(second))
    return changed, len(common_gids), new_in_second, missing_in_second


def print_report(
    changed: list[tuple[int, ListSignal, ListSignal, list[str]]],
    comparable_total: int,
    new_in_second: int,
    missing_in_second: int,
    sample_limit: int,
) -> None:
    triggered = len(changed)
    ratio = (triggered / comparable_total * 100.0) if comparable_total else 0.0

    print("\n=== comparison report ===")
    print(f"comparable_total = {comparable_total}")
    print(f"triggered        = {triggered}")
    print(f"trigger_ratio    = {ratio:.2f}%")
    print(f"new_in_second    = {new_in_second}")
    print(f"missing_in_second= {missing_in_second}")

    if comparable_total == 0:
        print("[WARN ] No comparable gids between two snapshots.")
        return

    if ratio == 0.0:
        print("[RESULT] Trigger ratio is 0.00%; this signal set is likely too weak here.")
    else:
        print("[RESULT] Non-zero trigger ratio observed.")

    if not changed:
        return

    print("\n=== changed samples ===")
    for idx, (gid, before, after, reasons) in enumerate(changed[:sample_limit], start=1):
        print(
            f"{idx:2d}. gid={gid} reasons={','.join(reasons)} "
            f"rating {before.rating_sig or '-'} -> {after.rating_sig or '-'} "
            f"tags {len(before.tags)} -> {len(after.tags)}"
        )


async def main() -> None:
    load_dotenv()

    parser = argparse.ArgumentParser(description="List signal trigger probe (rating/tags).")
    parser.add_argument(
        "--pages",
        type=int,
        default=10,
        help="How many cursor pages (next=<gid> hops) to fetch per snapshot.",
    )
    parser.add_argument("--wait-seconds", type=int, default=180, help="Wait between two snapshots.")
    parser.add_argument("--list-interval", type=float, default=1.0, help="Delay between page fetches.")
    parser.add_argument("--sample-limit", type=int, default=20, help="Max changed samples to print.")
    parser.add_argument(
        "--base-url",
        default=os.getenv("EX_BASE_URL", "https://e-hentai.org"),
        help="Base site URL, e.g. https://exhentai.org or https://e-hentai.org",
    )
    parser.add_argument(
        "--query-template",
        default=DEFAULT_QUERY_TEMPLATE,
        help="Initial path/query for the first page. Pagination then follows dnext (next=<gid>).",
    )
    parser.add_argument(
        "--inline-set",
        default=DEFAULT_INLINE_SET,
        help=(
            "Force list display mode via inline_set. "
            "Use dm_l/dm_p/dm_e or short form l/p/e. "
            "Use keep/off/none to preserve server default."
        ),
    )
    args = parser.parse_args()
    if "{page}" in args.query_template or "page=" in args.query_template:
        print("[WARN ] query-template contains page. EH pagination uses next=<gid>; page is ignored/removed.")

    cookies = parse_cookie_string(os.getenv("EX_COOKIES", ""))
    if cookies:
        print(f"[INFO ] cookies loaded: keys={sorted(cookies.keys())}")
    else:
        print("[INFO ] no cookies loaded from EX_COOKIES.")

    headers = {
        "User-Agent": (
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
            "AppleWebKit/537.36 (KHTML, like Gecko) "
            "Chrome/124.0.0.0 Safari/537.36"
        ),
        "Accept-Language": "en-US,en;q=0.9",
        "Referer": args.base_url,
    }
    resolved_inline_set = normalize_inline_set(args.inline_set)
    if args.inline_set.strip().lower() in {"keep", "off", "none"}:
        print("[INFO ] inline_set mode: keep server default")
    else:
        print(f"[INFO ] inline_set mode: {resolved_inline_set}")

    async with AsyncSession(
        headers=headers,
        cookies=cookies,
        allow_redirects=True,
        timeout=30,
        impersonate="chrome",
        verify=False,
    ) as client:
        first = await collect_snapshot(
            client,
            name="SNAPSHOT#1",
            pages=args.pages,
            base_url=args.base_url,
            query_template=args.query_template,
            inline_set=args.inline_set,
            list_interval=args.list_interval,
        )

        await controlled_wait(args.wait_seconds)

        second = await collect_snapshot(
            client,
            name="SNAPSHOT#2",
            pages=args.pages,
            base_url=args.base_url,
            query_template=args.query_template,
            inline_set=args.inline_set,
            list_interval=args.list_interval,
        )

    changed, comparable_total, new_in_second, missing_in_second = compare_snapshots(first, second)
    print_report(
        changed,
        comparable_total=comparable_total,
        new_in_second=new_in_second,
        missing_in_second=missing_in_second,
        sample_limit=args.sample_limit,
    )


if __name__ == "__main__":
    asyncio.run(main())
