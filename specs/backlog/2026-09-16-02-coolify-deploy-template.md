---
model: sonnet
effort: medium
---

# Coolify: a one-click SolidPing is gated on 1,000 GitHub stars, so ship the Coolify-ready compose now and park the upstream PR

> Shelved to `specs/backlog/` on 2026-09-16: the listing is blocked on the star
> gate, and the healthcheck subcommand it needs moved to spec
> `2026-09-16-03-casaos-app-store-listing` (section B).

## Problem

Coolify's one-click catalog is a compose file at `templates/compose/<service>.yaml`
in `coollabsio/coolify` (PR against the `next` branch), a logo at
`svgs/<service>.svg`, and a matching docs PR (`content/docs/services/<slug>.mdx`)
that must land together. Its contribution guide sets one hard eligibility rule:
**the service repository must have at least 1,000 GitHub stars**. SolidPing has
5 (checked 2026-09-16 with `gh api repos/fclairamb/solidping`). The listing is
blocked on stars, not on work.

Coolify users can still deploy any compose file through "Docker Compose Empty",
but nothing in our docs helps them:

- `web/docs/docs/installation/docker-compose.md:9-46` is a Postgres-first,
  dev-flavoured example (`LOG_LEVEL: debug`, a `postgres-data` volume) that
  needs `SP_FILESTORAGE_LOCAL_ROOT` spelled out. There is no single-container
  SQLite compose anywhere in the docs.
- Nothing in the image can answer a container healthcheck. The final stage is
  `gcr.io/distroless/base-debian13:nonroot` (`Dockerfile:118`): no shell, no
  curl. The server exposes `/api/mgmt/health`
  (`server/internal/app/server.go:2134-2135`, 503 during the shutdown window
  per `server.go:216`), but no process inside the container can call it. Coolify
  surfaces per-service health and waits on it during deploys; Uptime Kuma's
  template ships `extra/healthcheck` for exactly this. Without one, Coolify
  shows SolidPing as "running, health unknown".

Two things we do *not* need: a generated secret (when `SP_AUTH_JWT_SECRET` is
unset and the config still carries the `change-me-in-production` placeholder,
`ensureJWTSecret` generates one and persists it as a system parameter,
`server/internal/systemconfig/systemconfig.go:1441-1462`) and a public-URL
setting (none exists in `server/internal/config/config.go`; the dashboard is
served relative to whatever host reaches it).

## Proposal

### 1. Healthcheck: delivered by spec `2026-09-16-03-casaos-app-store-listing`

The `solidping healthcheck` subcommand and the Dockerfile `HEALTHCHECK` this
template relies on live in the CasaOS spec, section B. Nothing to build here.
If that spec has not landed when this one is picked up, lift its section B
into this spec first.

### 2. The Coolify compose file, kept in this repo

`deploy/coolify/solidping.yaml` is the exact file destined for
`templates/compose/solidping.yaml` upstream, so it is written to Coolify's
rules from day one:

```yaml
# documentation: https://solidping.io/docs/installation/coolify
# slogan: Uptime monitoring, alerting and status pages, self-hosted in one container.
# category: monitoring
# tags: monitoring,uptime,status-page,alerting,ssl,cron
# logo: svgs/solidping.svg
# port: 4000

services:
  solidping:
    image: ghcr.io/fclairamb/solidping:0.28
    environment:
      - SERVICE_URL_SOLIDPING_4000
    volumes:
      - solidping-data:/data
    # ICMP checks need a raw socket; drop this block if you never use ping checks.
    cap_add:
      - NET_RAW
    healthcheck:
      test: ["CMD", "/app/solidping", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  solidping-data:
```

Decisions baked in:

- **Image tag.** Coolify refuses floating tags. CI already publishes a
  `{major}.{minor}` tag (`.github/workflows/ci.yml:809`), so pin `:0.28`: it is
  a pinned line that still receives patch releases, the same trade Uptime
  Kuma's template makes with `:2`. Bumping it is a per-minor-release chore;
  add it to the pin list in `wiki/` next to the fleet pins.
- **`SERVICE_URL_SOLIDPING_4000` as a bare entry** is how Coolify assigns the
  generated domain to the `solidping` service and routes the proxy to port
  4000. Nothing in SolidPing consumes it; it is there for Coolify.
- **SQLite, single container.** The env block is empty on purpose and relies on
  the image defaults from spec `2026-09-15-10-docker-image-persist-data-defaults`
  (`/data` for the database and uploads), which is done. Postgres is a
  documented override, not a second template.
- **`NET_RAW`** per `web/docs/docs/features/check-types.md:207`.

Add a CI step that runs `docker compose -f deploy/coolify/solidping.yaml config`
so a malformed file cannot ship (Coolify magic variables are plain env names,
so `config` passes).

### 3. Docs page: `web/docs/docs/installation/coolify.md`

`sidebar_position: 6` (after Windows, `installation/windows.md:2`). Content, in
this order: what you get (one container, SQLite, a volume), the
"Docker Compose Empty" flow with the file pasted verbatim from
`deploy/coolify/solidping.yaml` (import it, do not copy it, so the two cannot
drift), set the domain, deploy, then first login with the seeded credentials
and the forced password rotation from `CLAUDE.md` "Default credentials".
One paragraph on switching to Postgres. No competitor content: the
`migrate-from-*` guides are the only competitor-facing docs allowed.

### 4. The upstream PR, prepared and parked

`wiki/distribution/coolify.md` (new; add `wiki/distribution/` to
`wiki/README.md`) records: the star gate and today's count, the three-part PR
recipe (template PR to `next` + `svgs/solidping.svg` from `res/logo.svg`, docs
PR, link them), the draft PR bodies, and the trigger "open both PRs the week
the repo crosses 1,000 stars". Keep the wiki page short; the template file is
the deliverable.

## Verification

- `make lint`, `make test` green.
- Local image build, then `docker inspect --format '{{.State.Health.Status}}'`
  reads `healthy` within the start period and `unhealthy` after a SIGTERM
  begins the shutdown window.
- `docker compose -f deploy/coolify/solidping.yaml up` on a machine without
  Coolify boots, persists across `down`/`up`, and the dashboard answers.
- One real "Docker Compose Empty" deploy on a Coolify instance (a throwaway
  VPS is enough) with a domain attached: the generated URL serves the login
  page over HTTPS and the service shows healthy. Record the Coolify version
  tested in the wiki page.

## Open questions

- Is `:0.28` the right pin, or should the file track `:latest` until the
  upstream PR exists (where the rule bites)? Default: `:0.28`, it is what the
  upstream file will need anyway.
- Should `NET_RAW` stay in the template or move to the docs as an opt-in?
  Default: keep it, commented as above; Coolify users are on VPSes where it is
  harmless.

## Delivery

Branch `feat/coolify-template`. PR title
`feat(deploy): Coolify-ready compose, healthcheck subcommand and docs`.
