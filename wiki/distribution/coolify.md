# Coolify one-click template

SolidPing's Coolify template is developed in this repo, at
[`deploy/coolify/solidping.yaml`](../../deploy/coolify/solidping.yaml), and documented
for direct use (Coolify's "Docker Compose Empty" import) at
[`installation/coolify.md`](../../web/docs/docs/installation/coolify.md). This page
covers the upstream catalog listing this template is destined for, which is blocked on
a star count, not on work.

## The star gate

Coolify's contribution guide (`coollabsio/coolify`) requires the service's repository
to have **at least 1,000 GitHub stars** before its template is accepted into
`templates/compose/`. SolidPing had **7 stars on 2026-09-22** — re-check with
`gh api repos/fclairamb/solidping --jq .stargazers_count` before acting on this page,
since the number above is a point-in-time snapshot, not a live value.

Nothing else blocks the listing. The template file, the healthcheck it depends on
(`server/internal/healthcheck/`, wired as the Dockerfile `HEALTHCHECK`), and
`linux/arm64` images are all already shipped.

## Prerequisites

- `ghcr.io/fclairamb/solidping` publishes `linux/amd64` **and** `linux/arm64` (the
  `docker` job in `.github/workflows/ci.yml`).
- The image has a container `HEALTHCHECK` via the `solidping healthcheck` subcommand —
  the distroless runtime has no shell/curl, so nothing else inside the container could
  call `/api/mgmt/health`.
- CI validates `deploy/coolify/solidping.yaml` with `docker compose config` on every
  change (the `docs` job), so the file can't silently rot before the PR is opened.

## The three-part PR recipe

Coolify's catalog PR against `next` needs three things to land together:

1. **The template**, copied verbatim from
   [`deploy/coolify/solidping.yaml`](../../deploy/coolify/solidping.yaml) to
   `templates/compose/solidping.yaml` in the fork. Bump the `image:` tag to whatever
   minor is current at PR time — the file in this repo tracks the latest release, but
   check `gh release list --limit 1` again right before copying, since some time will
   have passed since this page was last touched.
2. **The logo**, `res/logo.svg` in this repo, copied to `svgs/solidping.svg` in the
   fork.
3. **The docs PR**, `content/docs/services/solidping.mdx` against
   `coollabsio/coolify-docs` (a separate repository from the template PR) — adapted
   from `web/docs/docs/installation/coolify.md`, trimmed to what Coolify's own docs
   template expects. Link the two PRs to each other in their descriptions; Coolify's
   maintainers want both before merging either.

## Draft PR bodies

**Template PR** (`coollabsio/coolify`, against `next`):

> Adds a one-click template for [SolidPing](https://github.com/fclairamb/solidping), a
> self-hosted uptime monitoring, alerting and status-page tool. Single container,
> SQLite storage by default (Postgres is a documented override, not a second
> template), a container healthcheck via the distroless image's own `healthcheck`
> subcommand, and `SERVICE_URL_SOLIDPING_4000` for the generated domain. Docs PR:
> `coollabsio/coolify-docs#<fill in>`.

**Docs PR** (`coollabsio/coolify-docs`):

> Adds the installation page for the SolidPing template
> (`coollabsio/coolify#<fill in>`), mirroring
> `https://solidping.io/docs/installation/coolify`.

## Trigger

Open both PRs the week the repository crosses 1,000 stars. Don't pre-open them earlier
and let them sit — the image tag pin and the release notes would go stale waiting for
review.
