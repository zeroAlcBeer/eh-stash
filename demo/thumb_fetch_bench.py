"""
Bench three thumb-fetch strategies against s.exhentai.org.

Strategies:
  1. plain HTTP (no TLS) - try http:// scheme
  2. fresh HTTPS connection per request (mimics current scraper)
  3. HTTPS with a Session (keep-alive + conn pool)

Pulls 50 thumb_urls from Pi PG (random sample) and runs each strategy
against the same list, serially. Reports success rate, p50/p95/max
latency, total elapsed.

Run:
  cd /Users/zeroalcbeer/Documents/zeroAlcBeer/eh-stash
  python3 demo/thumb_fetch_bench.py
"""

import os
import sys
import time
import statistics
from pathlib import Path

import psycopg2
import requests

# ─── env loading ─────────────────────────────────────────────────────────────

ENV_PATH = Path(__file__).resolve().parent.parent / ".env"
if ENV_PATH.exists():
    for line in ENV_PATH.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))

EX_COOKIES_RAW = os.environ.get("EX_COOKIES", "")
if not EX_COOKIES_RAW:
    print("error: EX_COOKIES not set in env or .env", file=sys.stderr)
    sys.exit(1)

PI_DSN = os.environ.get("PI_DSN", "postgresql://postgres:postgres@192.168.0.110:5432/eh_stash")

def parse_cookies(raw: str) -> dict:
    out = {}
    for pair in raw.split(";"):
        pair = pair.strip()
        if "=" in pair:
            k, v = pair.split("=", 1)
            out[k.strip()] = v.strip()
    return out

COOKIES = parse_cookies(EX_COOKIES_RAW)
HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/124.0.0.0 Safari/537.36"
    ),
    "Accept-Language": "en-US,en;q=0.9",
    "Referer": "https://exhentai.org/",
}

# ─── pull URL sample ─────────────────────────────────────────────────────────

print(f"connecting to {PI_DSN.split('@')[-1]} ...")
conn = psycopg2.connect(PI_DSN)
with conn.cursor() as cur:
    cur.execute(
        "SELECT thumb_url FROM thumb_queue "
        "WHERE status IN ('done','pending') "
        "ORDER BY random() LIMIT 50"
    )
    URLS = [r[0] for r in cur.fetchall()]
conn.close()
print(f"sampled {len(URLS)} thumb URLs")
print(f"first: {URLS[0]}\n")

# ─── runner ──────────────────────────────────────────────────────────────────

def summarize(name: str, results: list, total_elapsed: float):
    latencies = [r["latency"] for r in results]
    ok = sum(1 for r in results if r["ok"])
    by_status = {}
    for r in results:
        by_status[r["status"]] = by_status.get(r["status"], 0) + 1
    latencies.sort()
    p50 = latencies[len(latencies)//2] if latencies else 0
    p95 = latencies[int(len(latencies)*0.95)] if latencies else 0
    print(f"=== {name} ===")
    print(f"  total elapsed:  {total_elapsed:6.2f}s")
    print(f"  success:        {ok}/{len(results)}")
    print(f"  per-req p50:    {p50*1000:6.0f} ms")
    print(f"  per-req p95:    {p95*1000:6.0f} ms")
    print(f"  per-req max:    {max(latencies)*1000:6.0f} ms")
    print(f"  status breakdown: {by_status}")
    print(f"  effective rate: {len(results)/total_elapsed:.2f} req/s")
    print()

def do_request(session_or_module, url: str, timeout: float = 30):
    t0 = time.monotonic()
    try:
        if isinstance(session_or_module, requests.Session):
            r = session_or_module.get(url, timeout=timeout, allow_redirects=False)
        else:
            r = session_or_module.get(
                url, cookies=COOKIES, headers=HEADERS,
                timeout=timeout, allow_redirects=False,
            )
        return {
            "latency": time.monotonic() - t0,
            "status": r.status_code,
            "ok": r.status_code == 200 and len(r.content) > 100,
            "size": len(r.content),
        }
    except requests.RequestException as e:
        return {
            "latency": time.monotonic() - t0,
            "status": f"err:{type(e).__name__}",
            "ok": False,
            "size": 0,
        }

# ─── strategy 1: plain HTTP ──────────────────────────────────────────────────

def run_plain_http():
    name = "1. plain HTTP (no TLS)"
    t0 = time.monotonic()
    results = []
    for u in URLS:
        http_u = u.replace("https://", "http://", 1)
        results.append(do_request(requests, http_u))
    summarize(name, results, time.monotonic() - t0)

# ─── strategy 2: fresh HTTPS per request ─────────────────────────────────────

def run_fresh_https():
    name = "2. HTTPS, fresh connection per request (no keep-alive)"
    t0 = time.monotonic()
    results = []
    for u in URLS:
        # force a fresh connection by using a brand new Session each iteration
        s = requests.Session()
        s.headers.update(HEADERS)
        s.cookies.update(COOKIES)
        s.headers["Connection"] = "close"
        results.append(do_request(s, u))
        s.close()
    summarize(name, results, time.monotonic() - t0)

# ─── strategy 3: HTTPS with keep-alive Session ───────────────────────────────

def run_session_https():
    name = "3. HTTPS, single Session with keep-alive"
    s = requests.Session()
    s.headers.update(HEADERS)
    s.cookies.update(COOKIES)
    t0 = time.monotonic()
    results = []
    for u in URLS:
        results.append(do_request(s, u))
    summarize(name, results, time.monotonic() - t0)
    s.close()

# ─── main ────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    only = sys.argv[1] if len(sys.argv) > 1 else None
    if only in (None, "1", "http"):
        run_plain_http()
    if only in (None, "2", "fresh"):
        run_fresh_https()
    if only in (None, "3", "session"):
        run_session_https()
