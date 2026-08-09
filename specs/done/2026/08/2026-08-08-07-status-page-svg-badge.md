---
model: sonnet
effort: medium
---

# Status badges exist per check, but not for a whole status page

## Problem

The badge subsystem ([server/internal/handlers/badges/](server/internal/handlers/badges/))
only serves **per-check** badges
(`GET /api/v1/orgs/:org/checks/:check/badges/:components`). There is no way to
embed a single "All systems operational" image for a whole status page in a
README, an email footer, or a wiki — the exact artifact users expect from a
status product, and the static sibling of the JS widget (spec 2026-08-08-08)
for contexts where scripts can't run (GitHub READMEs).

**Depends on 2026-08-08-05** (server-side page rollup) for the status value.

## Proposal

### Endpoint

`GET /api/v1/status-pages/:org/:slug/badge` — public SVG, registered with the
other public status-page routes ([server.go:1224](server/internal/app/server.go:1224)).

- Reuse the existing SVG machinery in
  [badges/svg.go](server/internal/handlers/badges/svg.go) (`GenerateSVG`,
  `ComposeBadgeSVG`) rather than writing new rendering code.
- Query params consistent with check badges
  ([handler.go:52](server/internal/handlers/badges/handler.go:52)): `label`
  (left-side text, default the page name), `style` (`flat` / `flat-square`),
  `width` / `minWidth` with the same bounds.
- Right-side text and color from the page rollup:
  `operational` → green, `degraded` → yellow, `down` → red,
  `maintenance` → blue, `unknown` → gray. English text in v1; localization out
  of scope.
- Same visibility gate as `ViewStatusPage`
  ([service.go:1344](server/internal/handlers/statuspages/service.go:1344)):
  disabled/private → 404. Same caching as check badges:
  `Content-Type: image/svg+xml`, `Cache-Control: public, max-age=60`.

### Dash0 embed UI

- Add a badge-embed block to the status page settings area (near
  [status-pages.$statusPageUid.appearance.tsx](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.appearance.tsx)),
  showing the badge preview plus copyable URL / Markdown / HTML snippets —
  mirror the existing per-check embed page
  ([badges.tsx:184](web/dash0/src/routes/orgs/$org/badges.tsx:184)) including
  its testid conventions (`badge-embed-url` / `-markdown` / `-html`).
- Follow the design reference
  ([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx));
  add i18n keys to all dash0 locales.

### Docs & tests

- Document the endpoint in `openapi.yaml` and
  [wiki/api-specification/status-pages.md](wiki/api-specification/status-pages.md);
  mention it on the status-pages docs page in `web/docs`.
- Handler tests: one per rollup state asserting the SVG contains the expected
  text/color; 404 on private/disabled pages (with a public positive control);
  cache header present.
