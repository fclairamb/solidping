# CasaOS / ZimaOS app store listing

SolidPing's CasaOS/ZimaOS store entry is developed in this repo, at
[`deploy/casaos/solidping/`](../../deploy/casaos/solidping/), and shipped to users by a
PR against the upstream store repo. This page covers the upstream PR mechanics and the
per-release maintenance that keeps the listing current. Product content (description,
compose file contents, decisions on running as root, the memory reservation) is
documented at the point of use in `deploy/casaos/solidping/docker-compose.yml` itself —
this page does not duplicate it.

Prerequisites this listing depends on, both delivered by
`specs/done/2026/09/2026-09-16-03-casaos-app-store-listing.md`:

- `ghcr.io/fclairamb/solidping` publishes `linux/amd64` **and** `linux/arm64` (the
  `docker` job in `.github/workflows/ci.yml`).
- The image has a container `HEALTHCHECK` via the `solidping healthcheck` subcommand
  (`internal/healthcheck`) — the distroless runtime has no shell/curl, so nothing else
  inside the container could call `/api/mgmt/health`.

## Upstream repo shape

`IceWhaleTech/CasaOS-AppStore` is a folder per app:
`Apps/<Name>/docker-compose.yml` plus `icon.svg`, an optional `thumbnail` and
`screenshot-{n}` files. Metadata lives in a single top-level `x-casaos` block in the
compose file (older entries carry a legacy per-service `x-casaos` block instead — the v2
build strips that shape, don't copy it). `./scripts/build_dist.sh` plus the repo's
`validator.yml` CI validate every PR. `en_US` alone is an accepted locale set — do not
machine-translate the description into every locale dash0 ships, the way some existing
entries did.

**Before opening a PR, re-read the upstream repo's own contribution guide.** It defers
implementation details to `docs/specs/*.md` in that repo, and those specs have moved at
least once already (a v1 → v2 protocol change touching the exact `x-casaos` shape
above). Anything below dated before that PR may be stale by the time you act on it.

## Opening the PR

1. Fork `IceWhaleTech/CasaOS-AppStore`.
2. Copy `deploy/casaos/solidping/` from this repo to `Apps/SolidPing/` in the fork,
   verbatim — `docker-compose.yml`, `icon.svg`, `thumbnail.png`, `screenshot-1.png`,
   `screenshot-2.png`, `screenshot-3.png`.
3. Fill in the two placeholders left in `docker-compose.yml` at PR time (they can't be
   filled before a release exists to point at):
   - the `image:` tag, `x-casaos.version` and `x-casaos.update_at` — all three move
     together, pinned to the SolidPing release that first published `linux/arm64` +
     the `healthcheck` subcommand (or whatever later release you are bumping to — see
     below).
   - `x-casaos.release_notes.en_US` — three bullets summarizing that release, written
     from its `CHANGELOG.md` entry (`wiki/conventions/changelog.md` has the entry-writing
     conventions; this is a compressed, non-technical restatement of the same facts, not
     a copy-paste of the changelog prose).
4. Run `./scripts/build_dist.sh` in the fork and confirm it produces
   `dist/index.json` and `dist/apps/io.solidping.solidping/` — this is what the
   upstream CI validates, so catching a failure locally first saves a review round-trip.
5. Open the PR titled "Add SolidPing" against `IceWhaleTech/CasaOS-AppStore`.

## Per-release bump

Every SolidPing release that should reach the CasaOS store needs a follow-up PR against
the fork (not every release — see below):

1. Update `deploy/casaos/solidping/docker-compose.yml` in **this** repo: bump the
   `image:` tag, `x-casaos.version`, `x-casaos.update_at`, and rewrite
   `x-casaos.release_notes.en_US` from the new release's `CHANGELOG.md` entry. Commit
   this alongside the release, the same way any other pinned-version file in the repo
   is bumped at release time.
2. Copy the updated `docker-compose.yml` (and any changed screenshot/icon) into the fork
   of `IceWhaleTech/CasaOS-AppStore` at `Apps/SolidPing/`, re-run
   `./scripts/build_dist.sh`, and open a PR against upstream.

Not every release needs step 2 immediately — batch it if you're shipping multiple
releases in a short window, the same way the other manually-bumped fleet image pins
in this project are handled — but don't let the listed version drift more than a
release or two behind, since `x-casaos.version` / `update_at` are what CasaOS shows the
user as freshness signals.

There is currently no single "release checklist" doc in this repo listing every
downstream pin that needs a bump (the `sp` CLI pins, the fleet image pins, and now this
CasaOS entry are each documented at their own point of use instead). If one gets
created, add this bump as a line item there.
