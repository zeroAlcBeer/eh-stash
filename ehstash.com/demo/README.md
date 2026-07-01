# demo — local pipeline

Lets you iterate on frontend design without touching Neon / R2.

```
vite :5173 ──► wrangler :8787 ──► docker pg :5433
                                       │
        ┌──────────────────────────────┘
        ▼
caddy thumbs :8080
```

Postgres + Caddy run in Docker (containers stay up between iterations).
Worker and frontend run natively for hot reload — they're the things you
want to edit.

## First-time setup

Once `data/data.dump` and `data/thumbs.tar` are in place (the fixtures
the main README mentions):

```bash
./demo/setup.sh
```

That takes ~1–2 minutes. It:

1. Starts `docker compose` postgres + caddy
2. Wipes & re-applies `schema/001_init.sql`
3. Restores the full dump
4. Subsamples to ~2000 rows + their group siblings
5. Extracts only the matching thumbs from the tar

Change the sample size:

```bash
SAMPLE_SIZE=500   ./demo/setup.sh   # tighter
SAMPLE_SIZE=0     ./demo/setup.sh   # everything (~157k thumbs, ~3.7GB)
```

## Day-to-day

```bash
# Two terminals:
(cd worker && npm run dev)       # http://localhost:8787
(cd frontend && npm run dev)     # http://localhost:5173
```

Postgres + Caddy stay running between dev sessions; just `docker compose
up -d` from `demo/` if they stopped.

## Reset

```bash
./demo/reset.sh        # drops pg volume + extracted thumbs
./demo/setup.sh        # rebuild
```

## Notes

- Postgres is on port **5433** (not the default 5432) to avoid clashing
  with anything else you might have running locally.
- Caddy serves with `Content-Type: image/jpeg` forced, mirroring how
  production R2 behaves (rclone sets this header on upload).
- Worker reads `localConnectionString` in `wrangler.toml` for `wrangler
  dev`; in production it goes through Hyperdrive to Neon.
- Frontend reads `.env.development` (committed) which points at the demo
  ports. `.env` / `.env.production` (gitignored) are for real deploys.
