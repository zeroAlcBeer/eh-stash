# CI / deploy + monitoring

How eh-stash builds, deploys, and is monitored — aligned with the `bangou`
and `konakore` repos.

---

## 1. GitHub Actions

One workflow under `.github/workflows/`:

| Workflow | Runner | Trigger | Job |
|---|---|---|---|
| `ci.yml` | GitHub-hosted | every PR + push to `master` | scraper (`go vet/build/test`), python (deps + byte-compile for api & pi-sync), frontend (`pnpm build`) |

Deploys are **manual** (§3) — there is no deploy workflow. The previous
`deploy.yml` ran on a self-hosted Pi runner that was lost in a disk
migration; the manual `make release-*` path replaces it permanently.

---

## 2. Independent component versioning

Each of the four deployable services is versioned by the 12-char git SHA:

- `make release-<api|scraper|frontend|pi-sync>` builds that one image and
  pushes `:<sha>` + `:latest` to the LAN registry (`192.168.0.110:5000`).
- The Pi stack pins the SHA per component in `/opt/stacks/ehstash/.env`
  (`API_TAG`, `SCRAPER_TAG`, `FRONTEND_TAG`, `PI_SYNC_TAG`), consumed by
  `docker-compose.pi.yaml`.
- A deploy rewrites only the changed components' `*_TAG` and rolls only
  those services (`docker compose pull <svc> && up -d`), never touching
  the floating postgres/pgvector tag.

Makefile recipes use the `docker compose` v2 plugin (`COMPOSE ?= docker
compose`) — the Pi has no `docker-compose` v1 binary.

---

## 3. Manual deploy runbook

Deploys run from a workstation (the Mac, arm64 — native builds match the
Pi), not CI:

1. Release the changed components:

   ```sh
   make release-api        # or release-scraper / release-pi-sync
   VITE_THUMB_BASE_URL=<r2-thumb-origin> make release-frontend
   ```

   `VITE_THUMB_BASE_URL` (the R2 thumb CDN origin) is baked into the
   frontend bundle at build time. Read the current value from the stack:
   `sudo grep ^VITE_THUMB_BASE_URL /opt/stacks/ehstash/.env`. Empty/missing
   falls back to local `/v1/thumbs`.

2. If `docker-compose.pi.yaml` changed, sync it to the stack and validate:

   ```sh
   scp docker-compose.pi.yaml pi:/tmp/compose.yaml
   ssh pi 'sudo cp /tmp/compose.yaml /opt/stacks/ehstash/compose.yaml &&
           cd /opt/stacks/ehstash && sudo docker compose config --quiet'
   ```

3. On the Pi, pin each released SHA in `/opt/stacks/ehstash/.env`
   (`API_TAG` / `SCRAPER_TAG` / `FRONTEND_TAG` / `PI_SYNC_TAG`).

4. Roll only the changed services — a **scoped** pull, never an unscoped
   `docker compose pull` (the floating postgres/pgvector tag must not be
   allowed to restart the DB on an unrelated deploy):

   ```sh
   cd /opt/stacks/ehstash
   sudo docker compose pull <svc> && sudo docker compose up -d
   ```

5. Verify all four health endpoints (§4) return 200.

---

## 4. Health endpoints

Every service exposes a liveness probe (host ports on the Pi):

| Service | Endpoint | Notes |
|---|---|---|
| api | `http://<pi>:3000/healthz` | cheap, no DB round-trip |
| scraper | `http://<pi>:6060/healthz` | shares the pprof listener |
| frontend | `http://<pi>:4173/` | vite preview root |
| pi-sync | `http://<pi>:8097/healthz` | daemon thread; real loop health is in the DB runtime heartbeat |

After a deploy, poll all four for HTTP 200 — a broken roll means the
release didn't take.

---

## 5. Uptime Kuma monitors (manual, register once)

Registering monitors is a one-time step in the Kuma web UI — it is not stored
in this repo. Add one **HTTP(s)** monitor per service pointing at the
endpoints above:

| Monitor name | URL | Expected |
|---|---|---|
| eh-stash / api | `http://192.168.0.110:3000/healthz` | 200 |
| eh-stash / scraper | `http://192.168.0.110:6060/healthz` | 200 |
| eh-stash / frontend | `http://192.168.0.110:4173/` | 200 |
| eh-stash / pi-sync | `http://192.168.0.110:8097/healthz` | 200 |

Suggested: 60s interval, 1 retry, group the four under an "eh-stash" monitor
group. Adjust the host if Kuma runs off the Pi's LAN.
