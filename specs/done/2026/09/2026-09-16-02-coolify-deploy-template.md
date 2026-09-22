---
model: sonnet
effort: medium
---

# Coolify: a one-click SolidPing is gated on 1,000 GitHub stars, so ship the Coolify-ready compose now and park the upstream PR

> Shelved to `specs/backlog/` on 2026-09-16, un-shelved to `specs/todos/` on
> 2026-09-22. The star gate still holds, so the upstream PR stays parked — but
> the dependency that made the work unbuildable is gone: spec
> `2026-09-16-03-casaos-app-store-listing` has landed, so the `healthcheck`
> subcommand (`server/internal/healthcheck/`), the Dockerfile `HEALTHCHECK`
> (`Dockerfile:163`) and `linux/arm64` images all exist. Everything below is
> buildable as written.

## Problem

Coolify's one-click catalog is a compose file at `templates/compose/<service>.yaml`
in `coollabsio/coolify` (PR against the `next` branch), a logo at
`svgs/<service>.svg`, and a matching docs PR (`content/docs/services/<slug>.mdx`)
that must land together. Its contribution guide sets one hard eligibility rule:
**the service repository must have at least 1,000 GitHub stars**. SolidPing has
7 (re-checked 2026-09-22 with `gh api repos/fclairamb/solidping`; it was 5 on
2026-09-16). The listing is blocked on stars, not on work.

Coolify users can still deploy any compose file through "Docker Compose Empty",
but nothing in our docs helps them:

- `web/docs/docs/installation/docker-compose.md:9-46` is a Postgres-first,
  dev-flavoured example (`LOG_LEVEL: debug`, a `postgres-data` volume) that
  needs `SP_FILESTORAGE_LOCAL_ROOT` spelled out. There is no single-container
  SQLite compose anywhere in the docs.
- ~~Nothing in the image can answer a container healthcheck.~~ **Resolved
  2026-09-22.** The final stage is still distroless (no shell, no curl), but
  the binary now probes itself: `solidping healthcheck`
  (`server/internal/healthcheck/`) calls `/api/mgmt/health` over loopback and
  the Dockerfile wires it as a container `HEALTHCHECK` (`Dockerfile:163`). It
  returns 503 during the graceful-shutdown window, which is the signal Coolify
  wants — it surfaces per-service health and waits on it during deploys. The
  template below only has to reference it.

Two things we do *not* need: a generated secret (when `SP_AUTH_JWT_SECRET` is
unset and the config still carries the `change-me-in-production` placeholder,
`ensureJWTSecret` generates one and persists it as a system parameter,
`server/internal/systemconfig/systemconfig.go:1441-1462`) and a public-URL
setting (none exists in `server/internal/config/config.go`; the dashboard is
served relative to whatever host reaches it).

## Proposal

### 1. Healthcheck: already done, nothing to build

Delivered by spec `2026-09-16-03-casaos-app-store-listing` (section B), which
landed before this spec was un-shelved: `server/internal/healthcheck/`,
`Dockerfile:163`, and `linux/arm64` in the `docker` job. Verify the three are
still in place, then move on to section 2.

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
    image: ghcr.io/fclairamb/solidping:0.31
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
  `{major}.{minor}` tag (`.github/workflows/ci.yml:809`), so pin the current
  minor line: it stays pinned while still receiving patch releases, the same
  trade Uptime Kuma's template makes with `:2`. Bumping it is a
  per-minor-release chore; add it to the pin list in `wiki/` next to the fleet
  pins. **Set this to the latest released minor at implementation time, not to
  the value written above** — `:0.31` was current on 2026-09-22 (latest release
  `v0.31.1`, with `v0.32.0` already open as a release PR), and this line goes
  stale roughly weekly. Check `gh release list --limit 1` first.
- **`SERVICE_URL_SOLIDPING_4000` as a bare entry** is how Coolify assigns the
  generated domain to the `solidping` service and routes the proxy to port
  4000. Nothing in SolidPing consumes it; it is there for Coolify.
- **SQLite, single container.** The env block is empty on purpose and relies on
  the image defaults from spec `2026-09-15-10-docker-image-persist-data-defaults`
  (`/data` for the database and uploads), which is done. Postgres is a
  documented override, not a second template.
- **`NET_RAW`** per `web/docs/docs/features/check-types.md:306` (the line moved
  since this spec was written) and the worked compose examples in
  `web/docs/docs/features/traceroute-diagnostics.md:136,144`.

Add a CI step that runs `docker compose -f deploy/coolify/solidping.yaml config`
so a malformed file cannot ship (Coolify magic variables are plain env names,
so `config` passes).

### 3. Docs page: `web/docs/docs/installation/coolify.md`

`sidebar_position: 6` (after Windows, `installation/windows.md:2`; still free
as of 2026-09-22 — `casaos.md` took 7 and `yunohost.md` took 8). Content, in
this order: what you get (one container, SQLite, a volume), the
"Docker Compose Empty" flow with the file pasted verbatim from
`deploy/coolify/solidping.yaml` (import it, do not copy it, so the two cannot
drift), set the domain, deploy, then first login with the seeded credentials
and the forced password rotation from `CLAUDE.md` "Default credentials".
One paragraph on switching to Postgres. No competitor content: the
`migrate-from-*` guides are the only competitor-facing docs allowed.

### 4. The upstream PR, prepared and parked

`wiki/distribution/coolify.md` (new; the `## Distribution` section already
exists at `wiki/README.md:106` alongside `casaos.md` and `yunohost.md`, so this
is one more line under it, not a new section) records: the star gate and the
count on the day you write it, the three-part PR
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

- ~~Is `:0.28` the right pin, or should the file track `:latest` until the
  upstream PR exists?~~ **Settled 2026-09-22: pin the current minor line.** It
  is what the upstream file needs anyway, and keeping `:latest` in the repo
  means the file would have to be rewritten at PR time. See the image-tag
  decision above for how to pick the value.
- Should `NET_RAW` stay in the template or move to the docs as an opt-in?
  Default: keep it, commented as above; Coolify users are on VPSes where it is
  harmless.

## Delivery

Branch `feat/coolify-template`. PR title
`feat(deploy): Coolify-ready compose and docs` (the healthcheck subcommand the
original title mentioned already shipped with the CasaOS spec).
