# eh-stash

A self-hosted ExHentai metadata index and a public read-only mirror
([ehstash.com](https://ehstash.com)), maintained in a single repo.

The same React frontend ships in two shapes via a Vite build flag:

| Mode | Command | What you get |
|------|---------|--------------|
| **Self-hosted** | `pnpm build` | Full app — Gallery, Favorites, Recommended, Admin, tag translation, scraper, pi-sync |
| **Public** | `pnpm build:public` | Read-only gallery — no Admin/Favorites/Recommended, age-gate modal, settings menu, cosplay toggle |

## Repository layout

```
eh-stash/
├── frontend/          # React + Vite SPA (shared by both modes)
├── api/               # Python FastAPI backend (self-hosted only)
├── scraper-go/        # Go scraper (self-hosted only)
├── pi-sync/           # Python Pi → Neon + R2 sync worker
├── migrations/        # PostgreSQL schema migrations
├── ehstash.com/       # Cloud-side components (public mode)
│   ├── worker/        # TypeScript Hono Worker (Cloudflare)
│   ├── schema/        # Neon SQL schema + blacklist
│   ├── scripts/       # Data import / tag export / thumb upload
│   └── demo/          # Docker Compose + Caddy for local public-mode testing
├── docs/              # Architecture notes and design docs
├── docker-compose.yaml
├── docker-compose.pi.yaml
└── Makefile
```

## Quick start — self-hosted

1. Copy `.env.example` → `.env` and fill in your ExHentai cookies and
   database credentials.
2. `make up` — starts PostgreSQL, API, scraper, and frontend.
3. Open `http://localhost:5173`.

See `docs/` for detailed architecture and sync-task documentation.

## Quick start — public (ehstash.com)

### Frontend

```bash
cd frontend
cp .env.public.example .env.public
# Fill in VITE_API_BASE_URL and VITE_THUMB_BASE_URL
pnpm install
pnpm build:public
```

### Worker

```bash
cd ehstash.com/worker
npm install
# Edit wrangler.toml — set your Neon DSN, R2 bucket, bindings
npx wrangler deploy
```

### Schema

Apply `ehstash.com/schema/001_init.sql` to your Neon database, then
optionally load `blacklist.sql`.

## How the mode flag works

`frontend/src/shared/mode.js` exports `IS_PUBLIC` which is `true` when
`import.meta.env.VITE_APP_MODE === 'public'`. This flag gates:

- **Routing** — Admin, Favorites, and Recommended routes are only
  registered in self-hosted mode.
- **Nav** — the corresponding nav links are hidden in public mode; a
  settings menu appears instead.
- **Filters** — public mode shows a curated category subset with Cosplay
  gated behind a user toggle; self-hosted shows all categories.
- **Favorites UI** — favorite rings, badges, and icons are hidden in
  public mode.
- **Welcome modal** — public mode shows an age-gate / info modal on
  first visit.

## i18n

The frontend supports `zh-CN`, `zh-TW`, and `en`. Locale is auto-detected
from `navigator.languages` at load time. All user-facing strings go
through `t()` in `shared/i18n.js` — no hardcoded Chinese in components.

## License

See [LICENSE](LICENSE).
