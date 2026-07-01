# Pi → Cloud sync pipeline — design brief

A handover document. Pick this up in a fresh session to design and
implement the periodic sync from the Pi-side `eh-stash` instance into
the cloud-side `ehstash.com` deployment (Neon Postgres + Cloudflare R2).

The cloud side is already live; the sync pipeline has not been built yet.
This file captures the deployment state and the architectural options
discussed so the next session can resume cold.

---

## 1. Repositories involved

| Repo | Path | Role |
|---|---|---|
| `eh-stash` (original) | `/Users/zeroalcbeer/Documents/zeroAlcBeer/eh-stash` | Pi-side scraper + local Postgres. Long-running, runs in Portainer on `192.168.0.110`. **Not deployed by this session — this is the upstream of the sync.** |
| `ehstash.com` (this) | `/Users/zeroalcbeer/Documents/zeroAlcBeer/ehstash.com` | Cloud-side read-only public site. Frontend on CF Pages, Worker on CF Workers, data on Neon, thumbs on R2. **Already live.** |

---

## 2. Current deployment state

### Cloud (ehstash.com)

| Component | Identifier | Status |
|---|---|---|
| Domain (CF Registrar) | `ehstash.com` | active |
| Frontend (CF Pages) | `https://ehstash.com`, `https://www.ehstash.com`, `https://ehstash.pages.dev` | production branch = `master`, deployed via `wrangler pages deploy` |
| Worker | `https://api.ehstash.com` (custom domain) + `https://ehstash-api.zeroalcbeer.workers.dev` | live, Hono + postgres-js, talks to Neon via Hyperdrive |
| Hyperdrive | id baked into `worker/wrangler.toml`, binding name `DB` | live, points at Neon pooled endpoint |
| R2 bucket | `eh-stash-thumbs`, public via `https://pub-f26b453e5bb04d5482f0b023bc0ece66.r2.dev` | 157,198 objects (initial bulk via rclone), object key = bare `gid` (no extension), Content-Type forced to `image/jpeg` on upload |
| Neon project | `ep-orange-dew-ao4vtx7j` in `ap-southeast-1` | free tier (191 compute-hours / 0.5 GB) |
| Rate limit | `[[ratelimits]]` binding, 100 req / 60 s per IP | live |
| Web Analytics | CF Pages built-in | enabled |
| Bot Fight Mode + Browser Integrity Check | dash toggles | enabled |
| Account 2FA | TOTP | enabled |

### Cloud schema (Neon)

`schema/001_init.sql` was applied. Two tables:

```sql
eh_galleries (
  gid, token, category, title, title_jpn, uploader, posted_at,
  language, pages, rating, fav_count, thumb, comment_count, tags JSONB,
  last_synced_at, is_active, base_title,
  row_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()  -- watermark for sync
)
gallery_group_members (
  group_id, gid UNIQUE REFERENCES eh_galleries(gid) ON DELETE CASCADE
)
```

`schema/blacklist.sql` was run once after import; 625 galleries
permanently deleted matching:

```
male:yaoi, female:amputee, female:futanari, other:full color,
female:pregnant + female:dark nipples (composite)
```

Worker also enforces the same blacklist at runtime in `buildBlacklistClause`.

Cosplay category is hidden by default; visible only when the frontend
sends `allow_cosplay=1` (controlled by the SettingsMenu checkbox stored
in `localStorage.ehstash:allow-cosplay`).

Current row counts (post-blacklist):
- `eh_galleries`: ~156,568
- `gallery_group_members`: ~46,573

### Pi (eh-stash) state

| Component | Status |
|---|---|
| Local Postgres on `192.168.0.110:5432` | live, full dataset |
| `scraper-go` container | live, scraping EX endlessly with 1 req/sec |
| Thumb storage | `/opt/eh-stash/thumbs/{gid}` bind-mount, ~3.7 GB of files |
| `row_updated_at` column on `eh_galleries` | **NOT yet added** — needs `ALTER TABLE` + backfill before sync starts |
| EX cookies | in `.env` next to docker-compose, scraper-go uses them |

---

## 3. The problem

The cloud side has a frozen snapshot from 2026-05-25. The Pi keeps
scraping new galleries from ExHentai. We need a recurring sync that:

1. Pushes new / updated gallery rows from Pi PG → Neon
2. Gets the thumbnails of newly synced galleries into R2

Constraint that drives the design:

> **EX thumbnails require a valid login cookie to download, and EX
> commonly blocks Cloudflare Worker egress IPs. Any thumb-fetching code
> must run from the Pi (where the cookie lives and where the IP is on
> EX's allow-list), not from CF Workers.**

The row push (Pi PG → Neon) is straightforward — no special access
needed beyond Neon credentials. The thumbnail acquisition is the part
that constrains architecture.

---

## 4. Sync cadence

Earlier discussion settled on **hourly** as the sweet spot:

- Neon free-tier compute is 191 hours/month. Each sync wakes the DB
  for ~5 minutes (cold start + work). Hourly × 24 × ~5 min = ~60 h/month.
  Safely inside free tier.
- Anything sub-hourly (10 min, 30 min) keeps the DB awake continuously
  (5-min idle timeout) → blows the free tier in ~9 days.
- Anything > 6 h hurts UX (new galleries take too long to appear).

User-visible latency target: ≤ 1 hour from EX publish.

Hyperdrive cache TTL on read side: 60 s (default), so freshly-pushed
rows are visible to users within ~1 min after sync.

---

## 5. Architecture B — two independent Pi containers (decoupled)

```
Pi local PG (with row_updated_at watermark)
  │
  │ hourly cron
  ▼
┌─────────────────────────┐         ┌──────────────────────────────┐
│  neon-sync container    │         │  thumb-uploader container    │
│  ~80 lines Python       │         │  ~80 lines Python            │
│                         │         │                              │
│  1. SELECT * FROM       │         │  1. SELECT gid, thumb FROM   │
│     eh_galleries WHERE  │         │     eh_galleries WHERE       │
│     row_updated_at > W  │         │     thumb_uploaded = FALSE   │
│  2. UPSERT into Neon    │         │     LIMIT 500                │
│     (pooled endpoint)   │         │  2. For each:                │
│  3. Save new W locally  │         │     - GET thumb URL w/cookie │
└─────────────────────────┘         │     - PUT to R2 (gid as key, │
                                    │       ContentType image/jpeg)│
                                    │     - UPDATE Neon            │
                                    │       SET thumb_uploaded=TRUE│
                                    └──────────────────────────────┘
```

### Required changes

- **Pi PG**: `ALTER TABLE eh_galleries ADD COLUMN row_updated_at`,
  backfill with `NOW()`, scraper-go's `UpsertGalleriesBulk` updated to
  set `row_updated_at = NOW()` on conflict.
- **Neon schema**: add `thumb_uploaded BOOLEAN DEFAULT FALSE`. Initialize
  existing 156k rows to TRUE (they're already in R2). Future rows default
  FALSE.
- **New code in ehstash.com repo**: `pi-sync/neon-sync/` and
  `pi-sync/thumb-uploader/` directories, each with `Dockerfile`,
  `requirements.txt`, `sync.py`. Both Python + boto3/psycopg2.
- **Pi docker-compose**: add two services pointing at the new images.

### Why B
- Decoupled — thumb failures don't block row sync, vice versa.
- Observable — `SELECT COUNT(*) FROM eh_galleries WHERE NOT thumb_uploaded`
  is the entire backlog dashboard.
- Idempotent — replay safe; flip `thumb_uploaded` back to FALSE to retry.
- No edits to the existing `scraper-go` Go codebase.

### Trade-offs of B
- Adds two new containers to the Pi compose stack.
- Pi still keeps `/opt/eh-stash/thumbs/` as before (scraper-go behavior
  unchanged). thumb-uploader fetches its own copy from EX directly —
  doubles the EX egress for thumbs (once by scraper, once by uploader).
  Could be optimized by uploader reading from disk instead of fetching,
  but then the architectures starts converging with C.
- Rate-limit coordination: scraper-go's 1 req/sec limiter is in-process
  and not shared. thumb-uploader needs its own limiter. Recommended:
  give uploader 0.5 req/sec OR run uploader in an off-window when
  scraper is quieter. Otherwise combined Pi egress could trip EX's
  abuse thresholds.

---

## 6. Architecture C — modify scraper-go to upload to R2 in place of disk (user preference)

```
Pi local PG
  │
  └── scraper-go (existing process, modified)
        │
        ├── parser pulls gallery list / detail from EX
        │     INSERT row into eh_galleries (local)
        │     INSERT gid into thumb_queue
        │
        └── thumb worker (existing, MODIFIED)
              pulls from thumb_queue
              fetches thumb from EX (existing rate limit, cookies, retry)
              ► CURRENT:  writes /data/thumbs/{gid}
              ► PROPOSED: PUT to R2 (gid as key, Content-Type image/jpeg)
                          marks thumb_queue row done

Separately:
neon-sync container
   periodic row push to Neon, independent of thumb path
```

### Required changes

- **`scraper-go/worker/thumb.go`**: replace local file write with R2 PUT.
  Use `github.com/aws/aws-sdk-go-v2/service/s3` (or `minio-go` — lighter).
  Endpoint = R2 S3 endpoint; bucket = `eh-stash-thumbs`; key = `gid`
  string.
- **New env vars** in scraper-go `.env`:
  - `R2_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com`
  - `R2_BUCKET=eh-stash-thumbs`
  - `R2_ACCESS_KEY_ID=...`
  - `R2_SECRET_ACCESS_KEY=...`
- **Pi PG**: add `row_updated_at` to `eh_galleries` (same as B).
- **Pi docker volume `thumbs_data`**: can be dropped after migration.
  The `api` container in old eh-stash that read `/data/thumbs` is no
  longer needed for the cloud site, but the old project still runs
  locally — decide if you want to keep both behaviors (write to disk
  AND R2) for a while, or cut over.
- **New code in ehstash.com repo**: only `pi-sync/neon-sync/`.
  No separate uploader.

### Why C
- Single point of contact with EX: scraper-go already knows about
  cookies, rate limits, 429/509 backoff, banned-IP detection.
  Don't reinvent any of that.
- One Pi egress stream to EX, not two. Cleaner network footprint.
- thumb_queue gives free retry-on-failure semantics for R2 PUT too —
  if PUT fails, mark queue row as pending and the worker retries.
- Eventually allows decommissioning the local `/opt/eh-stash/thumbs`
  bind mount and recovering ~3.7 GB on the Pi.

### Trade-offs of C
- Touches Go code in the upstream `eh-stash` repo. That repo has been
  stable; this means one more thing to maintain there.
- Requires adding an S3-compatible SDK to scraper-go (binary size +
  dependency). `minio-go` adds ~5 MB to the binary, reasonable.
- The old eh-stash project's `api` and `frontend` containers (Python +
  React) currently depend on `/data/thumbs` being a real directory. If
  you keep running the local stack for personal use, you'd either keep
  writing to both places (defeats purpose) or stop using the local
  frontend (probably fine — the public ehstash.com replaces it).
- Coupling: a deployment regression in scraper-go's R2 path takes both
  data scraping AND thumb publishing offline.

---

## 7. Decision points for the next session

1. **Architecture B vs C** — user leans C. Confirm or change.

2. **For C specifically**: do you want scraper-go to write to BOTH
   local disk and R2 during a migration period, or cut over to R2-only
   immediately?

3. **R2 token for Pi**: same token that was rotated post-launch (R2
   API token, Object Read+Write scoped to `eh-stash-thumbs`). Generate
   a new one specifically for the Pi to keep credential blast radius
   small. Stored in Pi's `.env` next to the existing EX cookies.

4. **Initialize `thumb_uploaded` (B only) or skip column (C)**: in
   architecture C, do we need a `thumb_uploaded` column at all? Likely
   no — scraper-go's `thumb_queue` already encodes the same state on
   Pi side.

5. **Pi PG `row_updated_at`**: when to add it. Doing it now (before
   any sync work) avoids backfilling later. The ALTER + backfill +
   scraper-go UPSERT tweak is a small but real edit in the upstream
   repo.

6. **First-sync handling**: when sync first runs, watermark is empty,
   so it would push EVERY row Pi has — potentially 200k+ UPSERTs into
   Neon in one go. Cap the initial batch (e.g., LIMIT 5000 per cycle,
   advance watermark gradually) so the first day doesn't burn Neon
   compute hours unnecessarily.

7. **Sync runs on Pi or elsewhere?** Pi has the data and the EX
   cookies. Easiest: Pi. But it's an underpowered host — make sure
   the new containers don't compete with scraper-go for CPU.

---

## 8. Suggested implementation order (whichever architecture)

```
Step 1. Pi PG migration:
  ALTER TABLE eh_galleries ADD COLUMN row_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
  CREATE INDEX idx_eh_galleries_row_updated_at ON eh_galleries (row_updated_at);
  -- backfill is implicit because of DEFAULT NOW(); all rows get same timestamp

Step 2. scraper-go small change:
  In UpsertGalleriesBulk, add `row_updated_at = NOW()` to the
  ON CONFLICT DO UPDATE SET clause. Verify with a unit test.

Step 3. neon-sync container (shared between B and C):
  Python + psycopg2. Hourly loop. Watermark in a local file or
  small SQLite. Idempotent. Caps at 5000 rows / cycle for the first
  catch-up period.

Step 4 (C only): scraper-go thumb worker R2 changes:
  Add minio-go. Replace os.WriteFile with PutObject. Update env vars.
  Test against a staging R2 bucket if possible, or against the live
  one with a throwaway gid.

Step 4 (B only): thumb-uploader container:
  Python. Hourly loop. Reads pending rows from Neon, fetches from EX,
  PUTs to R2, updates Neon flag.

Step 5. Operations:
  - Add the new container(s) to Pi's docker-compose
  - Generate Pi-specific R2 API token (smaller blast radius than the
    one you used for the initial bulk upload)
  - Monitor first sync cycle manually, verify row counts on Neon
    side match expected delta
```

---

## 9. What's NOT in scope of this pipeline work

- Search-engine SEO (intentionally suppressed via robots.txt — not
  revisiting)
- User accounts / favorites (cloud site is read-only by design)
- Realtime push to Neon (the constraint is Neon free-tier compute
  hours, hourly batching is the answer)
- Replacing the Pi as the EX scraper (it works; don't break it)

---

## 10. Reference: existing `scraper-go` files for architecture C

If going with C, the modification is mostly in:

- `scraper-go/worker/thumb.go` — main edit (replace file write with R2 PUT)
- `scraper-go/config/config.go` — add R2_* env loading
- `scraper-go/go.mod` — add `github.com/minio/minio-go/v7`
- Maybe a new `scraper-go/storage/r2.go` for the S3 client wrapper

Files for reference (in the eh-stash repo): the scraper already has
`db/db.go` with `UpsertGalleriesBulk`, `thumb_queue` table is defined
in `migrations/001_schema.sql` of that repo, and the rate limiter at
`scraper-go/ratelimit/` is what already throttles EX requests.
