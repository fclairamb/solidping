# Status page links show no rich preview in chats (Slack, Discord, iMessage, …)

## Problem

Sharing a status page URL — e.g.
`https://solidping.k8xp.com/status0/acmetech/abyla-windows-vms` — produces no
useful link preview in Slack, Discord, Teams, iMessage, X, LinkedIn, etc.

status0 is a SPA: `serveStatus0Static`
(`server/internal/app/server.go:1758`) returns the embedded
`status0res/index.html` verbatim for every non-asset path. Link-preview
crawlers do not execute JavaScript, so all they see is the static head of
`web/status0/index.html` — a generic `<title>SolidPing - Status Page</title>`,
no `og:*` / `twitter:*` tags, no description, no image. Every shared status
page link looks the same (or shows nothing at all), which is a bad look for a
product whose status pages are meant to be shared publicly.

## Proposal

Inject per-page Open Graph / Twitter Card metadata server-side when serving
the status0 `index.html` for a status-page path.

### Metadata injection

- In `serveStatus0Static` (or a wrapper), when the resolved file is the
  `index.html` fallback, parse the request path for the status-page route
  shapes `/status0/:org/:slug` and `/status0/:org` (default page).
- Resolve the page with the existing public lookup already used by
  `GET /api/v1/status-pages/:org/:slug` (`ViewStatusPage`,
  `server/internal/handlers/statuspages/handler.go:369`) — it handles org
  slug + page slug resolution without auth.
- Rewrite the served HTML head (string replacement on a marker or on
  `</title>` / `</head>` — no template engine needed), setting:
  - `<title>` — `"{Page name} — Status"` (or including org name)
  - `og:title` — same as title
  - `og:description` — the page's `Description` field
    (`server/internal/db/models/status_page.go:58`) when set; otherwise a
    fallback like `"Live status, uptime and incident history."`
  - `og:url` — canonical URL built from the request host + path
  - `og:type` = `website`, `og:site_name` = `SolidPing`
  - `twitter:card` = `summary` (or `summary_large_image` once an image ships)
  - `og:image` — see below
- All injected values must be HTML-escaped.

### og:image

Phase 1 (this spec): a static branded PNG shipped in the status0 assets
(crawlers do not render SVG — must be PNG/JPEG, ideally 1200×630).

Phase 2 (optional, can be a follow-up spec): a dynamically rendered PNG at
e.g. `/status0/:org/:slug/og.png` showing page name + current overall status
("All systems operational" / "N components down"), so the preview reflects
live state. `ViewStatusPage` already computes `overallAvailabilityPct`
(`server/internal/handlers/statuspages/service.go:171`); the open question is
PNG rendering in Go without heavy deps.

### Guardrails

- **No information leak**: for a non-existent, disabled, or non-public page,
  serve the generic unmodified `index.html` — identical response for "does
  not exist" and "not public" so metadata cannot be used to probe page
  existence.
- Keep the existing short cache (`max-age=60`) on the injected HTML; hashed
  static assets keep their 1-year cache — injection only touches the
  `index.html` fallback path.
- Non-status-page paths under `/status0/` (root, subscribe/confirm routes, …)
  keep the generic head.

### Tests

- Unit tests on the injection: correct tags for an existing public page,
  HTML escaping of name/description, generic head for missing / disabled /
  private pages, generic head for the status0 root.
- E2E/curl check: `curl /status0/:org/:slug | grep og:title` against the dev
  server, plus the Slack/Discord debuggers manually
  (opengraph.xyz, `https://cards-dev.twitter.com/validator`-style tools) once
  deployed.

## Implementation Plan

### 1. Static branded OG image (Phase 1)
- Ship a `1200×630` branded PNG at `web/status0/public/og-default.png` (the
  Vite build copies `public/*` into the embedded `status0res/`, so it is served
  at `/status0/og-default.png` with the 1-year asset cache). Also drop the file
  into `server/internal/app/status0res/og-default.png` so the currently embedded
  build carries it without a full `make build-status0`.

### 2. Server-side metadata injection (`server/internal/app/status0_meta.go`)
Pure, unit-testable helpers plus one `*Server` method:
- `statusPagePathParts(reqPath)` — parse the `/status0`-stripped path into
  `(org, slug, ok)`. Matches only the two status-page route shapes: one segment
  (`/:org`, default page) and two segments (`/:org/:slug`). Root, empty
  segments, and 3+ segments → `ok=false`.
- `requestOrigin(req)` — `scheme://host` from the request (`X-Forwarded-Proto`
  first token, else `req.TLS`, else `http`).
- `buildStatus0MetaTags(meta)` / `injectStatus0Meta(html, meta)` — build the
  escaped `<title>` + `og:*` / `twitter:*` / `description` block and splice it
  into the head: strip the static `<title>…</title>` and insert the block before
  `</head>`. All values HTML-escaped via `html.EscapeString`.
- `(*Server).status0MetaForPath(req, reqPath)` — resolve the page via the
  existing public lookups (`ViewStatusPage` / `ViewDefaultStatusPage`, which
  already enforce enabled + `visibility == "public"` and return
  `ErrStatusPageNotFound` otherwise). On any error → `ok=false` (identical
  generic response for missing / disabled / private — no existence leak). On
  success build `ogMetadata{Title: "{Name} — Status", Description: page desc or
  fallback, URL: origin+path, Image: origin+"/status0/og-default.png"}`.
- Constants: `ogSiteName="SolidPing"`, fallback description
  `"Live status, uptime and incident history."`, `twitter:card` =
  `summary_large_image` (a large image ships), `og:type=website`.

### 3. Wire into `serveStatus0Static` (`server/internal/app/server.go`)
- Add a `statusPagesService *statuspages.Service` field on `Server`; assign it
  where the service is already constructed (line ~1049).
- Track whether the index.html fallback branch was taken; only in that branch,
  and only when `status0MetaForPath` returns `ok`, rewrite the served bytes with
  `injectStatus0Meta`. Cache headers unchanged (`max-age=60` on the fallback,
  1-year on real assets).

### 4. Tests (`server/internal/app/status0_meta_test.go`)
- `statusPagePathParts`: root/one/two/three-segment + empty-segment cases.
- `buildStatus0MetaTags` / `injectStatus0Meta`: correct tags present; HTML
  escaping of name & description (`&`, `<`, `"`); default `<title>` removed;
  block inserted before `</head>`.
- `requestOrigin`: forwarded-proto, TLS, and plain cases.
- Table-driven, `t.Parallel()`, `testify/require`.

### 5. QA
- `make build-backend lint-back test`, plus a manual `curl` check when a dev
  server is available.
