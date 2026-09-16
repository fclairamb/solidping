---
model: sonnet
effort: medium
---

# CasaOS / ZimaOS app store: list SolidPing, which first needs an arm64 image and a decision on bind-mount ownership

## Problem

The CasaOS store (`IceWhaleTech/CasaOS-AppStore`, also what ZimaOS ships) is a
folder per app, `Apps/<Name>/docker-compose.yml` plus `icon.svg`, an optional
`thumbnail` and `screenshot-{n}` files, validated by `./scripts/build_dist.sh`
and the repo's `validator.yml` CI. Metadata lives in a top-level `x-casaos`
block; the per-service `x-casaos` blocks seen in older entries are legacy and
stripped by the v2 build. `en_US` alone is an accepted locale set. Uptime Kuma
is listed there under category `Networking`, pinned to an exact version, with
bind mounts under `/DATA/AppData/$AppID/...` and `network_mode: bridge`.

Three things stand between SolidPing and that folder:

1. **The main image is amd64-only.** The `Build and Push Docker Image` job
   (`.github/workflows/ci.yml:766-822`) sets no `platforms:` and no buildx;
   only the `sp` CLI image is multi-arch (`ci.yml:940-953`). CasaOS runs
   heavily on arm64 (Raspberry Pi, ARM NAS boxes), and the store's
   `architectures` field is read literally. The reason the image is single-arch
   is stale: `Dockerfile:85` and `Dockerfile:109-110` say CGO is needed for SQLite and
   builds with `CGO_ENABLED=1`, but the shipped driver is pure-Go modernc on
   `linux/{amd64,arm64}` (`server/internal/db/sqlitedriver/sqlitedriver.go:11-13`)
   and the release binaries already cross-build with `CGO_ENABLED=0`
   (`ci.yml:506`).
2. **Bind-mount ownership.** The image runs as `nonroot` (uid 65532,
   `Dockerfile:118`). CasaOS creates `/DATA/AppData/<id>/...` as root before
   the container starts, so the first SQLite write fails with `permission
   denied`. Uptime Kuma sidesteps this by running as root. There is no
   `PUID`/`PGID` convention in a distroless image.
3. **No container healthcheck.** The final image is distroless, no shell, no
   curl (`Dockerfile:118`). The server exposes `/api/mgmt/health`
   (`server/internal/app/server.go:2134-2135`, 503 during the shutdown window
   per `server.go:216`), but nothing inside the container can call it, and
   CasaOS shows container health in its UI. This spec adds the subcommand;
   the shelved Coolify spec (`specs/backlog/2026-09-16-02`) reuses it.

## Proposal

### A. Prerequisite: publish `ghcr.io/fclairamb/solidping` for `linux/arm64`

- `Dockerfile`: pin every builder stage to the build host
  (`FROM --platform=$BUILDPLATFORM ...`, including the three `node:24-alpine`
  stages, which only produce static assets and must not run under QEMU), add
  `ARG TARGETOS TARGETARCH` to `backend-builder`, and build with
  `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH`. Delete the stale CGO
  comment. Keep `distroless/base-debian13:nonroot` as the runtime (switching
  to `static` is a separate decision).
- `ci.yml` `docker` job: add `docker/setup-buildx-action` and
  `platforms: linux/amd64,linux/arm64`, mirroring `ci.yml:940-953` (the `sp`
  job, whose comment explains why buildx is mandatory).
- Verify with `docker buildx imagetools inspect ghcr.io/fclairamb/solidping:latest`
  listing both platforms, and by running the arm64 image on Apple silicon
  (`--platform linux/arm64`) through the smoke test from spec
  `2026-09-15-10`.

### B. Prerequisite: `solidping healthcheck` subcommand and a Dockerfile `HEALTHCHECK`

Add a `healthcheck` subcommand next to `serve` in the server binary. It GETs
`http://127.0.0.1:<port>/api/mgmt/health` where `<port>` comes from
`SP_SERVER_LISTEN` (default `:4000`, `config.go` server defaults), with a 3 s
timeout, and exits 0 on HTTP 200, 1 otherwise. No dependencies beyond
`net/http`. Then in the final Dockerfile stage:

```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/app/solidping", "healthcheck"]
```

The 503-during-shutdown behaviour is the wanted semantics: the orchestrator
sees the container go unhealthy before it stops.

Unit-test the subcommand against an `httptest` server returning 200 and 503.

### C. The store entry, kept in this repo

`deploy/casaos/solidping/` holds the exact files for the upstream
`Apps/SolidPing/` folder: `docker-compose.yml`, `icon.svg` (from
`res/logo.svg`), `thumbnail.png`, and `screenshot-1..3.png` copied from
`web/docs/static/showcase/01-checks-list.png`, `02-check-form-filled.png`,
`03-check-detail.png` (1280×800, regenerable via `make showcase`).

```yaml
name: solidping
services:
  solidping:
    image: ghcr.io/fclairamb/solidping:0.28.3
    container_name: solidping
    network_mode: bridge
    restart: unless-stopped
    user: "0:0"
    cap_add:
      - NET_RAW
    ports:
      - target: 4000
        published: "4000"
        protocol: tcp
    volumes:
      - type: bind
        source: /DATA/AppData/$AppID/data
        target: /data
    healthcheck:
      test: ["CMD", "/app/solidping", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
    deploy:
      resources:
        reservations:
          memory: "<from the last make bench-memory report>"

x-casaos:
  id: io.solidping.solidping
  main: solidping
  architectures: [amd64, arm64]
  category: Networking
  index: /
  port_map: "4000"
  scheme: http
  title: { en_US: SolidPing }
  tagline: { en_US: Uptime monitoring, alerting and status pages, self-hosted. }
  description:
    en_US: |
      <what it monitors (HTTP, TCP, DNS, ICMP, SSL, cron, databases, browser
      scripts...), alerting channels, status pages, multi-region workers,
      teams/orgs, and the Uptime Kuma import. Plain product description,
      no comparison table.>
  tips:
    before_install:
      en_US: |
        First login: admin@solidping.io / solidpass, organization "default".
        You are asked to set a new password immediately.
  developer: Florent Clairambault
  author: Florent Clairambault
  icon: icon.svg
  thumbnail: thumbnail.png
  screenshot_link: [screenshot-1.png, screenshot-2.png, screenshot-3.png]
  version: "0.28.3"
  update_at: "2026-09-16"
  release_notes: { en_US: "<three bullets from CHANGELOG.md for this version>" }
  website: https://www.solidping.io
  repo: https://github.com/fclairamb/solidping
  support: https://github.com/fclairamb/solidping/issues
  docs: https://solidping.io/docs
```

Decisions baked in:

- **`user: "0:0"` in this compose only.** The image stays `nonroot`; the CasaOS
  entry runs as root because CasaOS owns the bind-mount directory and its
  users click Install rather than `chown`. This matches Uptime Kuma and every
  other entry in that store. Distroless still has no shell. Say so in one line
  of the description. The alternative (a `tips.before_install` telling users
  to `chown 65532:65532 /DATA/AppData/...` over SSH) was rejected: it fails
  the "no terminal" audience the store exists for.
- **Exact version pin + `version` + `update_at` + `release_notes`.** The store
  wants all four to move together, so every SolidPing release needs a bump PR
  upstream. Add it to the release checklist in `wiki/` as another manual pin.
- **Memory reservation** comes from the latest `make bench-memory` report
  (`wiki/runbooks/memory-profiling.md`), not a guess. Cite the number and its
  date in the PR.
- **Nothing to generate**: the JWT secret is auto-generated and persisted when
  unset (`server/internal/systemconfig/systemconfig.go:1441-1462`), so the env
  block is empty. Spec `2026-09-15-10` (image defaults under `/data`) is
  done, so no database or storage env lines are needed either.
- **Locales**: `en_US` only. The dash0 locale files are not a source for store
  prose; do not machine-translate fifteen locales the way the Kuma entry did.

### D. Docs page: `web/docs/docs/installation/casaos.md`

`sidebar_position: 7`. Two paths: "from the store" (once merged) and "Install a
customized app" by pasting the compose from `deploy/casaos/solidping/docker-compose.yml`
(works today, before any upstream merge). Then the first-login paragraph. No
competitor content.

### E. Upstream PR and maintenance

`wiki/distribution/casaos.md`: fork, copy the folder to `Apps/SolidPing/`, run
`./scripts/build_dist.sh`, confirm `dist/index.json` and
`dist/apps/io.solidping.solidping/` appear, open the PR ("Add SolidPing"), and
the per-release bump recipe. Note that the repo's contribution guide defers
details to `docs/specs/*.md` upstream; re-read those at PR time, they moved
once already (v1 to v2 protocol).

## Verification

- CI publishes a two-platform manifest; the arm64 image boots on Apple silicon
  and writes under `/data`.
- `docker compose -f deploy/casaos/solidping/docker-compose.yml config` in CI.
- `./scripts/build_dist.sh` on a fork passes with the folder in place.
- A real install on a CasaOS or ZimaOS VM (x86 is fine) through "Install a
  customized app": the app tile opens the login page, data survives a
  container recreate, health shows green.
- `docker inspect --format '{{.State.Health.Status}}'` reads `healthy`
  within the start period and `unhealthy` once a SIGTERM begins the shutdown
  window; the subcommand has unit tests.
- `make lint`, `make test`.

## Open questions

- `distroless/static` instead of `base` now that CGO is off? Smaller, but check
  nothing needs libc (timezone data and CA certs are in both). Default: keep
  `base`, note the option.
- Does CasaOS accept `cap_add` untouched? It passes compose through Docker, so
  it should; confirm on the VM and drop `NET_RAW` to the docs if it is
  stripped.

## Delivery

Two PRs, in order: `fix(docker): publish for linux/arm64 and add a
HEALTHCHECK` (branch `fix/docker-arm64-healthcheck`, sections A and B), then
`feat(deploy): CasaOS app store entry and docs` (branch
`feat/casaos-listing`, sections C to E).
