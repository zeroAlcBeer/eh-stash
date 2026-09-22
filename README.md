# eh-stash

A self-hosted ExHentai metadata index: Go scraper + FastAPI backend +
React frontend, running on a Raspberry Pi.

Thumbnails are stored on Cloudflare R2 and served straight from the CDN
(`VITE_THUMB_BASE_URL` baked into the frontend build); `pi-sync` uploads
new thumbs and deletes the local copies — R2 is the source of truth.
The retired public mirror (ehstash.com: Cloudflare Worker + Neon) was
removed; only the R2 thumb pipeline remains.

## Repository layout

```
eh-stash/
├── frontend/          # React + Vite SPA
├── api/               # Python FastAPI backend
├── scraper-go/        # Go scraper
├── pi-sync/           # Python Pi → R2 thumbnail sync worker
├── migrations/        # PostgreSQL schema migrations
├── docs/              # Architecture notes and design docs
├── docker-compose.yaml
├── docker-compose.pi.yaml
└── Makefile
```

## Quick start

1. Copy `.env.example` → `.env` and fill in your ExHentai cookies and
   database credentials.
2. `make up` — starts PostgreSQL, API, scraper, and frontend.
3. Open `http://localhost:5173`.

See `docs/` for detailed architecture and sync-task documentation.

## i18n

The frontend supports `zh-CN`, `zh-TW`, and `en`. Locale is auto-detected
from `navigator.languages` at load time. All user-facing strings go
through `t()` in `shared/i18n.js` — no hardcoded Chinese in components.

## License

See [LICENSE](LICENSE).
