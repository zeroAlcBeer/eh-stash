# CI / deploy + monitoring

How eh-stash builds, deploys, and is monitored — aligned with the `bangou`
and `konakore` repos.

---

## 1. GitHub Actions

Two workflows under `.github/workflows/`:

| Workflow | Runner | Trigger | Job |
|---|---|---|---|
| `ci.yml` | GitHub-hosted | every PR + push to `master` | scraper (`go vet/build/test`), python (deps + byte-compile for api & pi-sync), frontend (`pnpm build`) |
| `deploy.yml` | self-hosted `[self-hosted, pi]` | push to `master` (path-filtered) + manual `workflow_dispatch` | release only changed components → pin tags in stack `.env` → sync compose → roll → verify health |

`deploy.yml` rebuilds **only the components whose paths changed** (via
`dorny/paths-filter`), so a frontend-only commit never rebuilds the scraper.
Force a release with the `workflow_dispatch` `components` input
(e.g. `api,pi-sync`).

> **Security (public repo):** `deploy.yml` runs on the Pi. It must never gain
> a `pull_request` trigger — only the repo owner can push `master`.

---

## 2. Independent component versioning

Each of the four deployable services is versioned by the 12-char git SHA:

- `make release-<api|scraper|frontend|pi-sync>` builds that one image and
  pushes `:<sha>` + `:latest` to the LAN registry (`192.168.0.110:5000`).
- The Pi stack pins the SHA per component in `/opt/stacks/ehstash/.env`
  (`API_TAG`, `SCRAPER_TAG`, `FRONTEND_TAG`, `PI_SYNC_TAG`), consumed by
  `docker-compose.pi.yaml`.
- `deploy.yml` rewrites the changed component's `*_TAG` to the new SHA and
  rolls only those services (`docker compose pull <svc> && up -d`), never
  touching the floating postgres/pgvector tag.

Makefile recipes use the `docker compose` v2 plugin (`COMPOSE ?= docker
compose`) — the Pi has no `docker-compose` v1 binary.

---

## 3. Pi self-hosted runner (one-time)

Self-hosted runners are registered **per account**. A runner serving another
account/repo will not pick up `zeroAlcBeer/eh-stash` jobs. Register one under
this repo with labels `[self-hosted, pi]`:

1. Repo → Settings → Actions → Runners → *New self-hosted runner* → follow
   the `./config.sh` steps, adding `--labels pi`.
2. The runner user needs: Docker access, LAN reachability to the registry,
   and passwordless `sudo` for the `sed`/`cp`/`docker compose` steps against
   `/opt/stacks/ehstash`.
3. Run it as a service (`./svc.sh install && ./svc.sh start`).

---

## 4. Health endpoints

Every service exposes a liveness probe (host ports on the Pi):

| Service | Endpoint | Notes |
|---|---|---|
| api | `http://<pi>:3000/healthz` | cheap, no DB round-trip |
| scraper | `http://<pi>:6060/healthz` | shares the pprof listener |
| frontend | `http://<pi>:4173/` | vite preview root |
| pi-sync | `http://<pi>:8097/healthz` | daemon thread; real loop health is in the DB runtime heartbeat |

`deploy.yml`'s Verify step polls all four for HTTP 200 before the job
succeeds, so a broken roll fails the deploy.

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
