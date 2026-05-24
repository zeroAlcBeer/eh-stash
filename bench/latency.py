# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "psycopg2-binary",
#   "requests",
# ]
# ///
"""
Integration-latency baseline + regression check for the three user-facing
endpoints that drive perceived performance:

  1. List page         GET /v1/galleries?page=1&page_size=100
  2. For You page      GET /v1/galleries?page=1&page_size=100&sort=recommended&min_fav=0&is_favorited=false
  3. Distribution      GET /api/v1/admin/recommended/distribution

Target: every endpoint p50 < 1000 ms.

Usage:
    uv run bench/latency.py

Override base URL or run count:
    uv run bench/latency.py --base http://192.168.0.110:4173 --n 5
"""

import argparse
import statistics
import sys
import time

import psycopg2
import requests


DEFAULT_BASE = "http://192.168.0.110:4173"
DEFAULT_PG = "postgresql://postgres:postgres@192.168.0.110:5432/eh_stash"
TARGET_MS = 1000

ENDPOINTS = [
    (
        "list",
        "/v1/galleries?page=1&page_size=100",
    ),
    (
        "for_you",
        "/v1/galleries?page=1&page_size=100&sort=recommended&min_fav=0&is_favorited=false",
    ),
    (
        "distribution",
        "/v1/admin/recommended/distribution",
    ),
]


def time_endpoint(base: str, path: str, n: int) -> list[float]:
    """Return list of elapsed seconds for n GETs."""
    url = base + path
    samples = []
    for _ in range(n):
        t0 = time.perf_counter()
        r = requests.get(url, timeout=60)
        elapsed = time.perf_counter() - t0
        if r.status_code >= 400:
            raise RuntimeError(f"{path} returned {r.status_code}: {r.text[:200]}")
        samples.append(elapsed)
    return samples


def db_snapshot(dsn: str) -> dict:
    """Capture a small DB state snapshot for the report header."""
    conn = psycopg2.connect(dsn)
    cur = conn.cursor()
    out: dict = {}
    try:
        cur.execute("SELECT COUNT(*) FROM eh_galleries WHERE is_active = TRUE")
        out["active_galleries"] = cur.fetchone()[0]
    except Exception as e:
        out["active_galleries"] = f"err: {e}"

    # tag_embedding may live on eh_galleries (pre-migration-008) or on
    # recommended_cache (post-migration-008). Try both.
    for table in ("eh_galleries", "recommended_cache"):
        try:
            cur.execute(
                f"SELECT COUNT(*) FROM {table} WHERE tag_embedding IS NOT NULL"
            )
            out[f"{table}.tag_embedding_not_null"] = cur.fetchone()[0]
        except Exception:
            conn.rollback()

    try:
        cur.execute(
            "SELECT embedding IS NOT NULL, liked_count FROM user_profile WHERE id = 1"
        )
        row = cur.fetchone()
        out["profile_ready"] = bool(row[0]) if row else False
        out["profile_liked_count"] = row[1] if row else 0
    except Exception as e:
        out["profile"] = f"err: {e}"

    try:
        cur.execute("SELECT COUNT(*) FROM tag_vocabulary WHERE is_active = TRUE")
        out["vocab_active"] = cur.fetchone()[0]
    except Exception:
        conn.rollback()

    # Post-migration-008 column: similarity in recommended_cache.
    try:
        cur.execute(
            "SELECT COUNT(*) FROM recommended_cache WHERE similarity IS NOT NULL"
        )
        out["recommended_cache.similarity_not_null"] = cur.fetchone()[0]
    except Exception:
        conn.rollback()

    cur.close()
    conn.close()
    return out


def summarize(name: str, samples: list[float]) -> tuple[str, bool]:
    ms = [s * 1000 for s in samples]
    p50 = statistics.median(ms)
    pmin, pmax = min(ms), max(ms)
    avg = statistics.mean(ms)
    pass_target = p50 < TARGET_MS
    marker = "PASS" if pass_target else "FAIL"
    line = (
        f"  {name:14s}  n={len(samples)}  "
        f"min={pmin:7.1f}ms  p50={p50:7.1f}ms  avg={avg:7.1f}ms  max={pmax:7.1f}ms  "
        f"[{marker} target <{TARGET_MS}ms]"
    )
    return line, pass_target


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default=DEFAULT_BASE)
    ap.add_argument("--pg", default=DEFAULT_PG)
    ap.add_argument("--n", type=int, default=5, help="samples per endpoint")
    args = ap.parse_args()

    print(f"== bench: {args.base}  pg={args.pg}  n={args.n} ==")
    snap = db_snapshot(args.pg)
    print("DB snapshot:")
    for k, v in snap.items():
        print(f"  {k:42s} = {v}")
    print("Results:")

    all_pass = True
    for name, path in ENDPOINTS:
        try:
            samples = time_endpoint(args.base, path, args.n)
            line, ok = summarize(name, samples)
            print(line)
            all_pass &= ok
        except Exception as e:
            print(f"  {name:14s}  ERROR: {e}")
            all_pass = False

    print()
    print("OVERALL:", "PASS" if all_pass else "FAIL")
    return 0 if all_pass else 1


if __name__ == "__main__":
    sys.exit(main())
