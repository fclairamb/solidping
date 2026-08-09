---
model: sonnet
effort: medium
---

# No lightweight public API for a status page's aggregated status

## Problem

Integrators who just want "is this service up right now?" have no cheap way to
ask. The only public JSON is the full status-page view
(`GET /api/v1/status-pages/:org/:slug`,
[service.go:1330](server/internal/handlers/statuspages/service.go:1330)), which:

- ships the entire page — sections, per-resource daily availability arrays and
  response-time series — when the caller wants one word;
- sets **no cache headers at all** (the badge handler already does
  `Cache-Control: public, max-age=60`,
  [handler.go:52](server/internal/handlers/badges/handler.go:52));
- is absent from the OpenAPI spec — none of the public status-page views are in
  [openapi.yaml](server/internal/app/openapi/openapi.yaml) (only the per-check
  badge endpoint is, ~line 1345), so the docs API reference doesn't mention
  them.

This endpoint is also the data source the embeddable widget
(spec 2026-08-08-08) needs.

**Depends on 2026-08-08-05** (server-side page rollup) for the `status` value.

## Proposal

### Endpoint

`GET /api/v1/status-pages/:org/:slug/summary` — public, registered with the
other public routes ([server.go:1224](server/internal/app/server.go:1224)),
following the existing `/:org/:slug/feed.xml` sub-path precedent.

No default-page shortcut: `/api/v1/status-pages/:org/summary` would collide
with `:slug` = `"summary"` on the existing two-segment route. Callers resolve
the default page's slug via `GET /api/v1/status-pages/:org` first.

### Response

```json
{
  "status": "operational",
  "counts": { "operational": 12, "degraded": 1, "down": 0, "maintenance": 0, "unknown": 0 },
  "page": { "name": "SolidPing", "slug": "main", "url": "https://status.example.com/" },
  "generatedAt": "2026-08-08T12:00:00Z"
}
```

- `status` / `counts` come straight from the spec-05 rollup.
- `page.url` is the canonical public URL: the custom domain when active,
  otherwise the absolute `/status0/{org}/{slug}` URL derived from the request
  host — same derivation the OG-meta injection uses
  ([status0_meta.go:110](server/internal/app/status0_meta.go:110)).
- camelCase throughout, per REST conventions.

### Behavior

- **Identical visibility gate** to `ViewStatusPage`
  ([service.go:1344](server/internal/handlers/statuspages/service.go:1344)):
  disabled or non-public page → `NOT_FOUND`, leaking nothing, not even
  existence.
- `Cache-Control: public, max-age=60`, mirroring the badge handler.
- No auth, no rate-limit exclusion needed: the per-IP limiter
  ([ratelimit.go:29](server/internal/middleware/ratelimit.go:29)) is fine here
  because widget/browser polls come from each visitor's own IP.

### Docs

- Add the summary endpoint to `openapi.yaml`, **and** document the existing
  public views while there (`/:org`, `/:org/:slug`, `/:org/:slug/feed.xml`) —
  they are currently missing.
- Update [wiki/api-specification/status-pages.md](wiki/api-specification/status-pages.md)
  ("Public views" section) and the status-pages feature page in `web/docs`.

### Tests

- Handler tests: response shape and counts against a fixture page; 404 for
  private and for disabled pages (with a public-page positive control);
  `Cache-Control` header present; custom-domain vs path-based `page.url`.
