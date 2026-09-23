---
model: sonnet
effort: medium
---

# The public status-page payload has no way to leave out the 3.4 MB of history a client will not render

## Problem

`GET /api/v1/status-pages/{org}` and `GET /api/v1/status-pages/{org}/{slug}`
return one shape: page, sections, resources, and — when the page enables them
— every resource's `availability` block: `dailyAvailability` (one point per
day of history) and `responseTimeSeries` (up to 100 points per region).
Measured on the dev deployment's 200-resource page:

| Payload | Bytes (uncompressed) |
|---|---|
| Full response | 3,168,037 |
| Without `responseTimeSeries` | 213,117 |
| Without `availability` entirely | 64,451 |

The TV wallboard (`/s/{org}/{slug}/tv`, spec `2026-08-29-08`) reads the page
name, `overallStatus`, `statusCounts`, `activeIncidents`, the page-level
`overallAvailabilityPct` and each resource's `check` block. It never touches a
series or a daily point, yet it polls the full shape every 30 s (15 s during
an incident). The cost is not only bytes: `enrichWithAvailability` is the
expensive half of the view (specs `2026-09-22-05` and `-06`), and a caller that
does not want it still pays for it.

The summary endpoint exists for the opposite end of the scale ("is it up",
five numbers) and the badge sits on it. Nothing sits in between.

## Decision

Both public page views accept an `include` query parameter naming the optional
payload sections the caller wants. Absent, everything is included — every
existing consumer keeps its payload byte-for-byte. The parameter can only
**narrow** what the page's own settings would produce, never widen it.

## API

`include` — optional, comma-separated, unordered, duplicates ignored. Tokens:

| Token | Adds | Server work it turns on |
|---|---|---|
| `availability` | per resource `availability.dailyAvailability`, `availability.overallAvailabilityPct`, `availability.period`, `availability.bucketUnit`; page-level `overallAvailabilityPct` | the `uptimebar` bucket query |
| `responseTime` | per resource `availability.responseTimeSeries` | the seam and rollup response-time fetch |

- Parameter absent → `availability,responseTime` (today's payload).
- `include=` (present, empty) → neither. A resource then has **no**
  `availability` key at all, and the page has no `overallAvailabilityPct`. The
  TypeScript type already declares both optional.
- `include=responseTime` alone → `availability` carries `responseTimeSeries`,
  `period` and `bucketUnit` and nothing else — the same shape
  `buildAvailabilityData` already emits for a page with `showAvailability`
  off.
- Unknown token → `400 VALIDATION_ERROR`, message naming the token and the
  valid set. Case-sensitive: the tokens mirror the JSON field names.
- `showAvailability` / `showResponseTime` in the response keep describing the
  **page's settings**, not the payload. A client that asked for less must not
  read them as "this page hides availability".
- Gating is unchanged: `include=availability` on a page whose operator set
  `showAvailability=false` returns no availability. The parameter is an
  intersection with the page settings.
- Caching is unchanged. The query string is part of every cache key; `Vary`
  stays as pinned by `statuspagecache`.
- The kiosk token (`?kiosk=`) and `include` compose in either order.

Document the parameter on both operations in
[`openapi.yaml`](server/internal/app/openapi/openapi.yaml) (`in: query`,
`style: form`, `explode: false`, `schema: {type: array, items: {enum:
[availability, responseTime]}}`) and in `wiki/api-specification/status-pages.md`
under the public reads. The docs site's API reference regenerates from the
OpenAPI document.

## Backend

- `statuspages.ViewOptions{Availability, ResponseTime bool}`; a
  `ParseViewOptions(url.Values) (ViewOptions, error)` next to the handler,
  exercised directly by tests. `ViewStatusPage` and `ViewDefaultStatusPage`
  take the options; the handlers map the parse error to
  `400 VALIDATION_ERROR` through `handlePublicError` so the gated
  `Cache-Control` still lands on the error.
- Inside the view, the existing shape is reused rather than re-plumbed: take a
  shallow copy of the page — exactly what `summaryAvailability` does with
  `lean.ShowResponseTime = false` — and AND the two flags with the options
  before the `if page.ShowAvailability || page.ShowResponseTime` branch. The
  copy is what `enrichWithAvailability` and the `OverallAvailabilityPct`
  gate read; `convertPageToResponse` keeps reading the original, which is
  what keeps `showAvailability` / `showResponseTime` truthful.
- Custom domains: `custom_domain_routing.go` allowlists the path prefix and
  passes the query through; add the routing test that
  `/api/v1/status-pages/{org}/{slug}?include=` on a custom host reaches the
  handler with the parameter intact.
- The MCP status-page tools and the `sp` CLI do not call the public view
  today (checked: no caller of the operation outside status0). No change.

## Frontend (status0)

- `usePublicStatusPage` / `useDefaultStatusPage` gain
  `include?: PublicPageInclude[]` in `PublicReadOptions`. `withKiosk`
  (`lib/kiosk.ts`) already picks `?` or `&` from the path it is given, so
  build the path with `include` first and hand it to `withKiosk` last, or
  move both onto `URLSearchParams`; either way one unit test pins the
  composed URL. The query key includes the sorted token list: the TV and the
  ordinary page must never share a cache entry for different shapes.
- The ordinary page (`status-page-view.tsx`) passes nothing and keeps the
  default. Being explicit there would break any cached older bundle that
  still calls without the parameter — not a real risk, but the default is
  the compatibility guarantee and the page is its first user.
- The TV route passes `include: []`. Its rendering changes are spec
  `2026-09-22-08`; this spec only adds the option and the URL.
- The embed widget reads `/summary` and is untouched.

## Tests

Backend, table-driven over both operations:

1. absent → both sections present (byte-identical to the existing golden
   payload test, if one exists; otherwise add one).
2. `include=` → no `availability` on any resource, no page-level percentage,
   and the counting fake DB records **no** results query at all.
3. `include=availability` → daily points, no series; `include=responseTime`
   → series, no daily points, no page-level percentage.
4. `include=availability,responseTime`, `include=responseTime,availability`,
   `include=availability,availability` → same as absent.
5. `include=foo` and `include=Availability` → 400 with the gated cache
   directive.
6. narrowing only: page with `showAvailability=false`, `include=availability`
   → no availability; `showAvailability` still `false` in the body.
7. kiosk + include on a `password` page: both orders unlock and narrow.
8. custom-domain passthrough (routing test).
9. `ParseViewOptions` unit test covering trimming and empty tokens
   (`include=availability,` → just availability).

status0: a unit test for the URL builder (kiosk + include, include alone,
neither), and the existing status-page e2e suite unchanged.

## Docs

- `openapi.yaml` and `wiki/api-specification/status-pages.md` as above.
- `web/docs/docs/features/status-pages.md`: one paragraph under the public
  API section — "fetch less" — with the TV as the example.
- Changelog entry (API addition, backwards compatible).

## Out of scope

- A `fields` mask over arbitrary keys. Two named sections cover the two
  expensive things; everything else in the payload is cheap.
- Changing the summary endpoint.
