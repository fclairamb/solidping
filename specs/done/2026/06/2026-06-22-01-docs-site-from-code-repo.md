# docs.solidping.io — co-locate the docs site in the code repo

> Infrastructure spec, not a product feature. Moves the existing Docusaurus
> documentation out of the separate `solidping-website` repo and into this repo
> as a fourth embedded frontend (`web/docs/`), served by the Go binary exactly
> like `dash0`/`status0` already are (`server/internal/app/server.go:116-126`,
> `serveStatus0Static` at `:1456`). The payoff is **co-location**: docs change in
> the same PR as the code they document, and the API reference is *generated*
> from the canonical `server/internal/app/openapi/openapi.yaml` — so it cannot
> drift. The marketing site (`www.solidping.io`) stays in `solidping-website`.

## Context

Today there are two public web surfaces, split across two repos, and the docs
live in the wrong one for keeping them current.

**`solidping-website` (separate repo) — Docusaurus 3.9.2 → `www.solidping.io`.**
Deployed via GitHub Pages (`.github/workflows/deploy.yml`,
`JamesIves/github-pages-deploy-action@v4`). It bundles three different things in
one site (`docusaurus.config.ts:14`, `url: https://www.solidping.io`):

- **User docs** (`docs/`): `intro.md`, `cli.md`, `installation/{docker,docker-compose,kubernetes,linux,windows}.md`,
  `configuration/{authentication,database,index,notifications,security}.md`,
  `features/{check-types,incidents,maintenance-windows,mcp,observability,on-call,status-pages}.md`.
  Served at `www.solidping.io/docs/*`. **Independently authored** — there is no
  sync from this repo today.
- **Marketing** (`src/pages/`): `index.tsx` landing, `saas/{slack,privacy,support,install-error}.md`, `terms.md`.
- **Blog** (`blog/`), plus `docusaurus-plugin-llms` (`docusaurus.config.ts:56`)
  emitting `llms.txt`.

**This repo (`solidping`) — Go backend that embeds and serves every frontend
itself.** `server/internal/app/server.go:116-126`:

```go
//go:embed all:res        → resFiles        (main/static)
//go:embed all:dash0res   → dash0Files      (dashboard, serveDash0Static  :1386)
//go:embed all:status0res → status0Files    (status pages, serveStatus0Static :1456)
//go:embed openapi/*      → openAPIFiles    (the API spec is ALREADY embedded)
```

Each frontend is a `web/<app>/` Vite+bun project; `make build` runs
`bun run build`, copies `dist/` into `server/internal/app/<app>res/`
(`Makefile:41-42`, `BACK_DASH0_RES`/`BACK_STATUS0_RES`; `build-dash0`/`copy-dash0`
at `:101`), and the Go binary serves it. Dev hot-reloads via a reverse proxy to
the Vite dev server with fallback to the embedded build
(`serveAppRedirect` `:1268`). Brand assets are pushed from `res/` into each app's
`public/` by `sync-brand-assets` (`Makefile:62-74`).

The canonical API contract is `server/internal/app/openapi/openapi.yaml`, which
already drives `oapi-codegen` for the Go server and client (`server/CLAUDE.md`,
`make generate`). There is also a hand-written `docs/api-specification.md` (39 KB)
in this repo's **engineering wiki** (`docs/` — `architecture.md`,
`database-model.md`, `competitors/`, `conventions/`, `research/`, …), which is a
private contributor reference, not user docs, and is almost certainly drifting
from the real spec.

**The gap:** because the user docs live in `solidping-website`, a code change here
and its documentation update land in two different repos, two different PRs, with
nothing enforcing they move together. And the API reference is hand-maintained
prose instead of being generated from the spec that already exists three
directories away.

## Decision — serving model (the one real design call)

The docs are static content. There are two ways to put them on
`docs.solidping.io` from this repo:

**A. Embed + serve from the Go binary** (mirrors `dash0`/`status0`). Build
Docusaurus → copy `build/` into `server/internal/app/docsres/` → `//go:embed` →
serve via Go, routed by `Host`.
- *Pro:* consistent with the entire existing architecture (everything is embedded
  and self-served); **version-matched** — the binary serves the docs for its own
  version, a real feature for a self-hostable OSS product; no new hosting infra;
  one deploy artifact.
- *Con:* a docs typo needs a binary rebuild (already true for every UI change
  here); public docs availability is tied to the app server unless also mirrored
  to a CDN.

**B. Build in CI and deploy to a CDN / Pages** (Cloudflare Pages, Netlify, GH
Pages on this repo).
- *Pro:* docs deploy independently of backend releases; CDN-fast; always-up.
- *Con:* a second hosting surface outside the binary; breaks the "ships with the
  code" property; inconsistent with how `dash0`/`status0` work.

**Decision: A (embed + Go-serve).** It is the literal realization of the stated
goal — *docs linked to the code* — taken all the way to "ships in the same
artifact," and it reuses the embed-and-serve pattern already proven by
`dash0`/`status0`. Co-location (same repo, same PR) is delivered either way; A
adds version-matched self-host docs for free. The rebuild-on-typo cost is real but
identical to every existing UI change here. **Mirroring the same `build/` to a CDN
for a fast, always-up public `docs.solidping.io` is an optional enhancement
(Slice 6), not a fork in the design** — one extra CI step pushing the same
artifact.

`baseUrl` stays `/` and the site owns the `docs.solidping.io` host (Host-based
routing in Go), rather than living under a `/docs/` path — this avoids a
redundant `docs.solidping.io/docs/` and the baseUrl-rewrite breakage that
edge-prefixing a static site causes.

## Goal

`docs.solidping.io` is served by the SolidPing Go binary from
`web/docs/`, an embedded Docusaurus instance in **this** repo. Its content is the
user docs moved out of `solidping-website`, plus an **API reference generated at
build time from `server/internal/app/openapi/openapi.yaml`** (no cross-repo
fetch — a relative path). A request with `Host: docs.solidping.io` returns the
docs at the host root; the dashboard and API on the primary host are unaffected.
`solidping-website` shrinks to a marketing-only site whose "Documentation" nav
points at the new subdomain. Updating a feature and its docs is now one PR in one
repo.

## Non-goals

- **Competitor / "compare" pages.** Those are marketing and belong on
  `www.solidping.io` (`solidping-website`, `src/pages/compare/*`) — separate work,
  not in this spec.
- **Rewriting the docs.** This is a *move*, not a content rewrite. Only mechanical
  edits (cross-repo links, `baseUrl`, frontmatter) are in scope. `cli.md` stays
  hand-written for now (generating it from the `urfave/cli` tree is a follow-up).
- **The blog.** Stays on `www.solidping.io` (marketing/SEO surface).
- **Docusaurus content versioning** (per-release `v1.0`/`v1.1` doc trees) — future.
- **Serving docs at a `/docs/` path on instances without a subdomain.** v1 is
  Host-routed at the docs host (`baseUrl: '/'`); a second `baseUrl: '/docs/'` build
  for path-mounted self-host docs is a follow-up.
- **Auth / access control on the docs.** They are public.
- **A full dev-proxy integration of the docs dev server** through the Go binary
  (as `dash0`/`status0` do). v1 dev serves Docusaurus directly on `:3000`; Go
  Host-routing is exercised against the built binary (see Slice 4).

## Design

Six independently committable slices. Slices 0 and 6 are optional and can be
skipped or deferred without blocking the rest.

### Slice 0 (optional, recommended): rename the wiki `docs/` → `wiki/`

With a public docs *site* arriving at `web/docs/`, the private engineering wiki
also being called `docs/` at the repo root is a foot-gun (two "docs"). Rename the
root `docs/` directory to `wiki/` and update references: root `CLAUDE.md`,
`server/CLAUDE.md`, `web/dash0/CLAUDE.md`, spec files under `specs/` that link
`docs/…`, and `.github/workflows/auto-update.yml`. This is a mechanical sweep, no
behaviour change.

**If bundling the rename is too noisy,** skip this slice and name the new site
`web/docs-site/` instead of `web/docs/`. The rest of the spec assumes the
recommended path (`wiki/` + `web/docs/`); substitute the site path if deferring.

### Slice 1: relocate the Docusaurus instance into `web/docs/`

Move the docs half of `solidping-website` into this repo:

```
web/docs/
├── docusaurus.config.ts     # url: https://docs.solidping.io, baseUrl: '/', docs at routeBasePath '/'
├── sidebars.ts
├── package.json             # @docusaurus/core 3.9.2, preset-classic, docusaurus-plugin-llms, + openapi plugin (Slice 3)
├── tsconfig.json
├── docs/                    # MOVED: intro, cli, installation/, configuration/, features/
├── src/css/                 # theme (brand tokens)
└── static/                  # docs-specific assets + brand (favicon/logo from res/, via sync-brand-assets)
```

Config changes from the `solidping-website` original:

- `url: 'https://docs.solidping.io'`, `baseUrl: '/'`.
- The docs plugin serves at the **root** (`routeBasePath: '/'`) — docs *are* the
  site now, not a `/docs/` sub-section.
- Drop the blog and `src/pages` (they stay on `www`); keep `docusaurus-plugin-llms`
  so `docs.solidping.io/llms.txt` is produced.
- Set `trailingSlash` explicitly (e.g. `false`) so the Go static handler
  (Slice 4) has a deterministic path→file mapping.
- Keep building with **bun** (matches `web/dash0`, `web/status0`).

The docs site is **outside the dash0 design system** — it is a separate
Docusaurus theme, so the dash0 `design-reference.tsx` rule in `CLAUDE.md` does not
govern it. Apply brand tokens (logo, colours) for visual consistency with `www`
and the app, but it is not a dash0 component change.

### Slice 2: build + embed wiring (Makefile)

Mirror the `status0` targets exactly.

- `Makefile`: add `BACK_DOCS_RES := $(BACK_DIR)/internal/app/docsres/`.
- `build-docs`: `cd web/docs && bun install && bun run build` (Docusaurus outputs
  to `web/docs/build/`, not `dist/`).
- `copy-docs`: copy `web/docs/build/` → `server/internal/app/docsres/`.
- `dev-docs`: `cd web/docs && bun run start` (Docusaurus dev server on `:3000`) —
  a standalone target; **not** folded into `make dev` by default (the Docusaurus
  dev server is heavy; run it when working on docs).
- Add all three to `.PHONY`; add `build-docs copy-docs` to the `build` chain
  (`Makefile:60`).
- Extend `sync-brand-assets` (`Makefile:62-74`) to also seed `web/docs/static/`
  (or `public/`) with `res/logo.*` + favicons.
- Commit a placeholder `server/internal/app/docsres/index.html` (or `.gitkeep`
  pattern matching how `dash0res`/`status0res` keep `//go:embed` compilable before
  the first build).

### Slice 3: generated API reference from `openapi.yaml`

Add `docusaurus-plugin-openapi-docs` + `docusaurus-theme-openapi-docs` to
`web/docs`, pointed at the in-repo spec by **relative path** —
`../../server/internal/app/openapi/openapi.yaml` — so there is no cross-repo
fetch. The generator (`docusaurus gen-api-docs`) emits MDX into the docs tree at
build time; wire it as a prebuild step in `build-docs` (and `make generate`, next
to the existing oapi-codegen step, for local regen).

This is the load-bearing reason co-location was chosen: the spec, the Go server
types, the generated client, *and* the published API reference now all derive from
one file in one repo. The hand-written `wiki/api-specification.md` is **retired as
the user-facing API doc** (it may stay as an internal note or be deleted).

### Slice 4: serve the embedded docs by Host in Go

`server/internal/app/server.go`:

- Add the embed + handle:
  ```go
  //go:embed all:docsres
  var docsFiles embed.FS
  ```
- Add `serveDocsStatic`, modelled on `serveStatus0Static` (`:1456`) **but with
  static-site (MPA) path resolution, not SPA index-fallback**: Docusaurus emits a
  real `.html` per route. Resolve a request path by trying, in order,
  `docsres/<path>`, `docsres/<path>/index.html`, `docsres/<path>.html`, and
  finally `docsres/404.html` with a 404 status. (This is the key difference from
  `dash0`/`status0`, which fall back to a single SPA `index.html`.)
- **Host routing:** at the router entry, dispatch requests whose `Host` matches
  the configured docs host to `serveDocsStatic` at root, ahead of the existing
  path-based app routing (`serveAppRoot` `:1246`). Everything else is unchanged.

`server/internal/config/`:

- Add `server.docsHost` (default `docs.solidping.io`; empty disables Host-routing,
  leaving docs reachable only via the Slice 6 build artifact).
- `docs_host` is a **multi-word koanf key**, so it needs an entry in the manual
  `SP_*` env reader — register `SP_DOCS_HOST` there (see the documented koanf
  multi-word-env quirk; without it the env override silently no-ops).

**Dev:** v1 serves the Docusaurus dev server directly at `localhost:3000` via
`make dev-docs`; the Go Host-routing path is verified against the built binary
(Slice "Verification"). A `Host`-matched dev redirect rule (extending
`RedirectRule` with an optional `Host` field so `serveAppRedirect` proxies the dev
docs host to `:3000` with embedded fallback) is a clean follow-up for full
`make dev` parity but is out of scope here.

### Slice 5: `solidping-website` becomes marketing-only (cross-repo)

In the **`solidping-website`** repo (separate PR there, coordinated with this one):

- Remove the `docs/` tree (it now lives in `web/docs/` here).
- Drop the docs preset (or reduce `preset-classic` to blog + pages); keep
  `src/pages` (landing, `saas/*`, `terms`) and `blog/`.
- Repoint nav/footer links (`docusaurus.config.ts:90-164`): `Documentation` →
  `https://docs.solidping.io`, `Installation` →
  `https://docs.solidping.io/installation/docker`, `Configuration` →
  `https://docs.solidping.io/configuration`.
- **Add `301` redirects** from the old `www.solidping.io/docs/*` URLs to
  `docs.solidping.io/*` (Docusaurus client redirects or an edge rule) to preserve
  bookmarks and SEO.

### Slice 6 (optional): CDN mirror for the public subdomain

If public `docs.solidping.io` should be CDN-fast and independent of app uptime,
add one CI step that pushes the same `web/docs/build/` artifact to a static host
(Cloudflare Pages / Netlify / GH Pages on this repo) in addition to embedding it.
Same artifact, second sink — not a redesign. Without this, `docs.solidping.io` is
served by the production SolidPing instance via Slice 4.

## CI

`.github/workflows/ci.yml`: add a `web/docs` build step so a broken docs build or
broken link fails CI (Docusaurus fails the build on broken internal links by
default — free link validation). With Decision A the docs build is already part of
`make build`, so the binary build covers it; an explicit `bun run build` of
`web/docs` gives a faster, isolated signal.

## Files to create / modify

### New (this repo)
- `web/docs/**` — the relocated Docusaurus instance (config, sidebars, package.json,
  tsconfig, `docs/**`, `src/css`, `static/**`) + the OpenAPI plugin config.
- `server/internal/app/docsres/` — embed target (build artifact; committed
  placeholder so `//go:embed` compiles).

### Modified (this repo)
- `server/internal/app/server.go` — `//go:embed all:docsres` + `docsFiles`,
  `serveDocsStatic` (MPA path resolution), Host-routing dispatch for `docsHost`.
- `server/internal/config/**` — `server.docsHost` + `SP_DOCS_HOST` in the manual
  env reader.
- `Makefile` — `BACK_DOCS_RES`, `build-docs`/`copy-docs`/`dev-docs`, `.PHONY`,
  `build` chain, `sync-brand-assets`, api-docs gen step.
- `.github/workflows/ci.yml` — build `web/docs`.
- *(Slice 0)* rename `docs/` → `wiki/` and update `CLAUDE.md` (root,
  `server/`, `web/dash0/`), `specs/**` links, `.github/workflows/auto-update.yml`.
- *(retire)* `wiki/api-specification.md` as the public API doc (superseded by the
  generated reference).

### Cross-repo (`solidping-website`)
- Remove `docs/`; reduce to blog + pages; repoint nav to `docs.solidping.io`;
  add `301` redirects from `/docs/*`.

## Verification

- **Build + embed:** `make build` succeeds with `web/docs` building and
  `docsres/` populated; the binary embeds it (`go:embed` compiles).
- **Host routing (built binary):**
  `curl -s -H 'Host: docs.solidping.io' http://localhost:4000/` returns the docs
  home; `-H 'Host: docs.solidping.io' …/installation/docker` returns that page;
  `…/llms.txt` is present; a missing path returns Docusaurus `404.html` with HTTP
  404. The same paths **without** the docs `Host` hit the normal app routing
  (dashboard at `/`, API under `/api`), unchanged.
- **Generated API reference:** the `/api` section renders endpoints from
  `openapi.yaml`; editing the spec and rebuilding changes the rendered reference
  (proves the no-drift property).
- **Config:** `SP_DOCS_HOST` override is honoured (set it to a test host, confirm
  routing follows) — guards against the koanf multi-word-env quirk.
- **Docs build hygiene:** Docusaurus broken-link check passes; `trailingSlash` and
  the Go static resolver agree on every route.
- **Cross-repo:** `solidping-website` builds without the `docs/` tree; nav links
  resolve to `docs.solidping.io`; an old `www.solidping.io/docs/intro` issues a
  `301` to `docs.solidping.io/intro`.
- `make lint && make test` (backend); `web/docs` build in CI.

## Risk log

| Risk | Mitigation |
|---|---|
| Docs typo → binary rebuild/redeploy (Decision A) | Already true for every UI change here; optional CDN mirror (Slice 6) decouples public docs; dev edits hot-reload via `dev-docs` |
| Public `docs.solidping.io` availability tied to the app server | Optional Slice 6 CDN mirror of the same artifact for always-up, CDN-fast public docs |
| Docusaurus is MPA (real `.html` per route), not SPA — a `dash0`-style index fallback would mis-serve every deep link | `serveDocsStatic` does static path resolution (`<path>` → `/index.html` → `.html` → `404.html`); `trailingSlash` set explicitly |
| `baseUrl`/host coupling — edge-prefixing a static site under `/docs/` breaks absolute asset URLs | `baseUrl: '/'`; the site owns the `docs.solidping.io` host via Go Host-routing, no path prefix |
| `SP_DOCS_HOST` silently ignored (multi-word koanf key) | Register it in the manual `SP_*` env reader, per the documented koanf quirk; verified explicitly |
| SEO / bookmark loss from `www/docs/*` → `docs.solidping.io/*` move | `301` redirects from old paths (Slice 5); `llms.txt` + sitemap regenerated on the new host |
| `docs/` (wiki) vs `web/docs/` (site) naming confusion in one repo | Slice 0 renames the wiki to `wiki/`; fallback names the site `web/docs-site/` |
| Brand drift between `www` (Docusaurus) and `docs` (Docusaurus) | Both pull brand assets from `res/` via `sync-brand-assets`; shared CSS tokens optional |
| Cross-repo coordination (website + backend ship together) | Sequence: land docs subdomain live from this repo first, then flip `solidping-website` nav + add `301`s |
| `go:embed all:docsres` fails when the dir is empty/missing before first build | Commit a placeholder in `docsres/`, mirroring how `dash0res`/`status0res` stay embed-compilable |
| API-ref generator drifts from oapi-codegen | Both read the same `openapi.yaml`; run `gen-api-docs` in `make generate` next to oapi-codegen so they regenerate together |
