# /// script
# requires-python = ">=3.11"
# dependencies = ["psycopg2-binary"]
# ///
"""
Force-rebuild user_profile with the new TF-IDF formula, then recompute
recommended_cache.similarity for every gallery.

The worker normally does this on ProfileUpdate signal (sent at the end of a
favorites-sync task). This script is a manual trigger so you don't have to
wait for/run a favorites sync just to apply the new formula.

Pure SQL — re-implements db/embeddings.go RebuildUserProfile + RecomputeAllScores
verbatim against the DB.

Usage:
    uv run bench/force_rebuild.py
    uv run bench/force_rebuild.py --pg postgresql://...
"""

import argparse
import sys
import time

import psycopg2

DEFAULT_PG = "postgresql://postgres:postgres@192.168.0.110:5432/eh_stash"
EMBEDDING_DIM = 65536

# Mirrors RebuildUserProfile in scraper-go/db/embeddings.go. The (1 + ln(tf))
# term is the sub-linear damping that prevents common tags (e.g. female:
# schoolgirl_uniform appearing in many favorites) from dominating the profile.
REBUILD_PROFILE_SQL = f"""
WITH tf AS (
    SELECT t.ns, tag_value AS tag, COUNT(*)::INT AS cnt
    FROM user_favorites f
    JOIN eh_galleries g ON g.gid = f.gid,
         jsonb_each(g.tags) AS t(ns, vals),
         jsonb_array_elements_text(vals) AS tag_value
    WHERE g.is_active = TRUE
    GROUP BY t.ns, tag_value
),
weighted AS (
    SELECT v.dim,
           v.idf * v.type_weight * (1.0 + LN(tf.cnt::float)) AS val
    FROM tf
    JOIN tag_vocabulary v
      ON v.namespace = tf.ns AND v.tag = tf.tag AND v.is_active = TRUE
),
total AS (
    SELECT SQRT(SUM(val * val)) AS norm FROM weighted
),
sparse_body AS (
    SELECT string_agg(
        (w.dim + 1)::text || ':' || (w.val / NULLIF(t.norm, 0))::text,
        ',' ORDER BY w.dim
    ) AS body
    FROM weighted w CROSS JOIN total t
),
liked AS (
    SELECT COUNT(*)::INT AS cnt
    FROM user_favorites f
    JOIN eh_galleries g ON g.gid = f.gid
    WHERE g.is_active = TRUE
)
UPDATE user_profile
SET embedding = CASE
        WHEN (SELECT body FROM sparse_body) IS NULL THEN NULL
        ELSE ('{{' || (SELECT body FROM sparse_body) || '}}/{EMBEDDING_DIM}')::sparsevec
    END,
    liked_count = (SELECT cnt FROM liked),
    updated_at = NOW()
WHERE id = 1;
"""

# Mirrors RecomputeAllScores.
RECOMPUTE_SCORES_SQL = """
UPDATE recommended_cache rc
SET similarity = NULLIF(1 - (rc.tag_embedding <=> up.embedding), 'NaN'::float8),
    updated_at = NOW()
FROM user_profile up
WHERE up.id = 1 AND rc.tag_embedding IS NOT NULL;
"""

CLEAR_ORPHAN_SCORES_SQL = """
UPDATE recommended_cache
SET similarity = NULL
WHERE tag_embedding IS NULL AND similarity IS NOT NULL;
"""


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--pg", default=DEFAULT_PG)
    args = ap.parse_args()

    conn = psycopg2.connect(args.pg)
    conn.autocommit = False
    cur = conn.cursor()

    print("[1/3] Rebuilding user_profile with TF-IDF formula...")
    t0 = time.perf_counter()
    cur.execute(REBUILD_PROFILE_SQL)
    profile_ms = (time.perf_counter() - t0) * 1000
    cur.execute("SELECT liked_count, embedding IS NOT NULL FROM user_profile WHERE id = 1")
    liked, ready = cur.fetchone()
    print(f"      done in {profile_ms:.0f}ms  liked_count={liked}  embedding_ready={ready}")

    print("[2/3] Recomputing recommended_cache.similarity for all galleries...")
    t0 = time.perf_counter()
    cur.execute(RECOMPUTE_SCORES_SQL)
    n_recomputed = cur.rowcount
    recompute_ms = (time.perf_counter() - t0) * 1000
    print(f"      done in {recompute_ms:.0f}ms  rows_updated={n_recomputed}")

    print("[3/3] Clearing orphan similarities (rows without embedding)...")
    cur.execute(CLEAR_ORPHAN_SCORES_SQL)
    n_cleared = cur.rowcount

    conn.commit()
    cur.close()
    conn.close()

    print(f"      cleared {n_cleared} stale similarities")
    print()
    print("OK. Now reload For You and the distribution panel to see new ordering.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
