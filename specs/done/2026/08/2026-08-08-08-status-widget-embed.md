---
model: opus
effort: high
---

# No embeddable live status widget for customers' own sites

## Problem

The only ways to surface SolidPing status on an external site are the full
status page (link or iframe) and per-check SVG badges. What customers actually
want on their marketing site is the small live pill — "⊙ All systems
operational" — linking to the status page: the Better Stack / Instatus
pattern. An SVG badge can't match the host site's theme, has no link semantics,
and can't expand; the full page is far too heavy to embed.

There's also a strategic angle already on the roadmap
([wiki/roadmap.md:90](wiki/roadmap.md:90)): a widget on customer sites is a
distribution channel, and white-labeling it is a natural paid entitlement
later.

**Depends on 2026-08-08-06** (public summary endpoint) as its data source.

## Proposal

### Embed snippet

```html
<script async src="https://<host>/embed/v1/widget.js" data-page="org/slug"></script>
```

- **Inline mode (default):** the script renders a small status pill where the
  tag sits — icon + label, colored by status, wrapped in a link to the status
  page (`page.url` from the summary response, so custom domains work).
- **Floating mode:** `data-mode="floating"` with
  `data-position="bottom-right" | "bottom-left"` renders a fixed-position pill
  (the classic corner badge).
- Customization via data-attributes only: `data-theme="light" | "dark" | "auto"`
  (auto follows `prefers-color-scheme`), and optional per-state label overrides
  (`data-label-operational`, `data-label-degraded`, `data-label-down`,
  `data-label-maintenance`, `data-label-unknown`) — built-in English defaults.

### Widget implementation

- Vanilla JS/TS compiled to a **single self-contained IIFE** — no framework, no
  external assets, target a few KB gzipped. Renders into **shadow DOM** so host
  page CSS can't break it and its CSS can't leak out.
- Polls `GET /api/v1/status-pages/:org/:slug/summary` every 60 s (matching that
  endpoint's `max-age=60`). Plain uncredentialed `fetch` — the wildcard CORS
  middleware ([server.go:1467](server/internal/app/server.go:1467)) already
  allows it; the widget must never send credentials (wildcard origin +
  credentials is rejected by browsers anyway).
- Graceful degradation: fetch failure or 404 renders nothing (never an error
  state on the customer's site).

### Serving & versioning

- Built as a separate entry in the status0 build (or a small esbuild step in
  the Makefile), embedded in the Go binary like the SPAs, served at
  `/embed/v1/widget.js` with `Cache-Control: public, max-age=3600`.
- **Everything under `/embed/v1/` is a frozen public contract** — once
  customers paste the snippet it can never break. Behavior changes go to
  `/embed/v2/`. Keep the v1 attribute surface deliberately minimal.

### Dash0 embed UI

- An "Embed" section on the status page settings (shared home with the spec-07
  badge snippets), generating the copy-paste `<script>` tag with the chosen
  mode/theme/position — mirroring
  [badges.tsx:184](web/dash0/src/routes/orgs/$org/badges.tsx:184) and the
  design reference. i18n keys in all dash0 locales.

### Entitlement hook (not implemented here)

The pill itself carries no SolidPing branding in v1; if attribution is added
later (e.g. in a hover/expanded state), its removal is the white-label paid
entitlement flagged in the roadmap. Note the hook, build nothing.

### Tests

- Go test: `/embed/v1/widget.js` is served with the right content type and
  cache header.
- Playwright E2E: a fixture HTML page embedding the widget against the test
  server — asserts the pill renders in shadow DOM with the expected status
  text and link, in both inline and floating modes, and that a 404 page slug
  renders nothing.
- Dash0 E2E for the snippet generator UI.

### Out of scope

Iframe embeds, per-check widgets, uptime graphs inside the widget, full
localization (label overrides cover it for v1), and the white-label
entitlement itself.

---

## Implementation Plan

1. **Widget source** — `web/status0/src/embed/widget.ts`: a vanilla-TS IIFE
   (no React, no imports) that reads its own `<script>` tag's data-attributes,
   derives the API origin from its own `src`, polls
   `GET /api/v1/status-pages/:org/:slug/summary` every 60 s with an
   uncredentialed `fetch`, and renders a pill into a shadow root. Security:
   `textContent`-only DOM writes, a static (non-interpolated) stylesheet, and
   an http/https scheme check on `page.url` before it becomes an `href`.
   Graceful degradation: nothing is rendered until the first successful fetch,
   so a 404 / network failure leaves the host page untouched.
2. **Build** — `bun build` step appended to status0's `build` script emitting a
   minified IIFE at `web/status0/dist/embed/v1/widget.js`. It rides the
   existing `build-status0` / `copy-status0` Makefile targets and the existing
   `status0-dist` CI artifact, so no new embed directory or CI artifact is
   needed.
3. **Serving** — `mainGroup.GET("/embed/v1/widget.js", s.serveEmbedWidget)` in
   `server/internal/app/server.go`, reading `status0res/embed/v1/widget.js`
   from the embedded FS and setting `Content-Type: application/javascript;
   charset=utf-8` + `Cache-Control: public, max-age=3600`. `/embed/v1/` is a
   frozen contract; a behavior change means a new `/embed/v2/` route.
4. **CI placeholder** — add the widget file to `.github/workflows/ci.yml`'s
   embed-placeholder step (same precedent as `docsres/llms.txt`) so the Go test
   passes on the lint/test job, which has no real status0 build.
5. **Dash0 UI** — `StatusPageWidgetCard` in
   `web/dash0/src/components/shared/status-page-widget-card.tsx`, rendered next
   to `StatusPageBadgeCard` on the status page appearance route. Mode / theme /
   position selects drive a copy-paste `<script>` snippet, reusing the badge
   card's copy-button + `<code>` pattern and the design reference's
   Card/Select/Label primitives. i18n keys under `statusPages.widget` in en, fr,
   de, es.
6. **Tests**
   - Go: `TestEmbedWidgetJS` in `server/internal/app/` — 200, correct
     content-type, `public, max-age=3600`, and the IIFE body.
   - Go: `TestEmbedWidgetSourceIsXSSSafe` — asserts the shipped widget source
     never uses `innerHTML`/`outerHTML`/`insertAdjacentHTML` and validates the
     link scheme, i.e. hostile labels and a `javascript:` `page.url` can't
     reach the DOM.
   - Playwright (status0): `web/status0/e2e/embed-widget.spec.ts` — seeds a
     public status page, serves a fixture HTML page on a *foreign* origin via
     `page.route`, and asserts inline + floating pills render in shadow DOM
     with the right text and link, that a hostile label is rendered as text and
     a hostile `page.url` is dropped rather than becoming a `javascript:` href,
     and that an unknown page slug renders nothing.
   - Playwright (dash0): extend `status-page-appearance.spec.ts` with the
     snippet-generator assertions.
7. **Entitlement hook** — a comment in the widget source marking where
   attribution/white-label would attach. Nothing built.
