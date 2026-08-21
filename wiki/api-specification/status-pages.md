# Status Pages

Status pages, their sections and resources, subscribers (double opt-in),
status updates, and the public read surfaces.

## Pages

### GET /api/v1/orgs/:org/status-pages
List status pages. Auth: required

### POST /api/v1/orgs/:org/status-pages
Create a status page. Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid
Get a status page. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid
Update a status page. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid
Delete a status page. Auth: required

## Sections

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections
List sections of a status page. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections
Create a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/reorder
Reorder the sections of a status page in one call (body carries the ordered
uid list). Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Get a section. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Update a section. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Delete a section. Auth: required

## Resources

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources
List resources in a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources
Add a resource to a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/reorder
Reorder the resources within a section in one call. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid
Update a resource. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid
Remove a resource. Auth: required

## Subscribers

Subscription is **double opt-in**: anyone can request a subscription from the
public status page, but nothing is delivered until the emailed confirmation
link is followed. Double opt-in is the primary anti-abuse control, which is why
`POST …/subscribers` is public while listing and deleting are not.

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers
List the subscribers of a status page. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers
Request a subscription. Sends a confirmation email; creates nothing deliverable
until confirmed. Auth: **public**

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers/:uid
Remove a subscriber. Auth: required

### GET /api/v1/public/status-subscribers/confirm
Confirm a subscription from the emailed link (signed token). Auth: public

### GET /api/v1/public/status-subscribers/unsubscribe
Unsubscribe from the emailed link (signed token). Auth: public

## Status updates

Human-written incident/maintenance narrative published on the status page. All
require auth.

### GET /api/v1/orgs/:org/status-updates
List status updates.

### POST /api/v1/orgs/:org/status-updates
Create a status update (notifies confirmed subscribers).

### GET /api/v1/orgs/:org/status-updates/:uid
Get a status update.

### PATCH /api/v1/orgs/:org/status-updates/:uid
Update a status update. `sectionUid`, `checkUid`, `incidentUid` and `linkUrl` are
presence-aware nullable fields: an omitted key leaves the column untouched, an
explicit `null` clears it, and a non-empty value is validated (section belongs
to the update's status page, check is a resource of it) and set. `""` clears
`linkUrl` (browser inputs yield `""`, not `null`) but is a `VALIDATION_ERROR` for
the three UID fields — send `null` to clear those instead. Clearing `sectionUid`
never implicitly clears `checkUid`.

### DELETE /api/v1/orgs/:org/status-updates/:uid
Delete a status update.

## Public views

A disabled or non-public page, and a page that doesn't exist, all return an
identical `NOT_FOUND` — none of these routes leak page existence.

### GET /api/v1/status-pages/:org
View the default status page for an organization. Auth: public. Same payload
shape as `GET /api/v1/status-pages/:org/:slug`, resolved to the org's default
page.

### GET /api/v1/status-pages/:org/:slug
View a specific status page by slug. Auth: public. Full payload: sections,
per-resource live status, and (when enabled) availability/response-time
history. Also carries `overallStatus` and `statusCounts` — the page-level
rollup computed server-side (see below) — but sets no `Cache-Control` header.

### GET /api/v1/status-pages/:org/:slug/summary
Lightweight "is it up?" companion to the full view above. Auth: public. Same
visibility gate, and the rollup comes from the exact same live data via the
shared `RollupPageStatus` helper, so it can never disagree with the full view
or the SVG badge. Sets `Cache-Control: public, max-age=60`.

```json
{
  "status": "operational",
  "counts": { "operational": 12, "degraded": 1, "down": 0, "maintenance": 0, "unknown": 0 },
  "page": { "name": "SolidPing", "slug": "main", "url": "https://status.example.com/" },
  "generatedAt": "2026-08-08T12:00:00Z"
}
```

`status` is one of `operational | degraded | down | maintenance | unknown`.
`page.url` is the canonical public URL: the verified custom domain when
active, otherwise the absolute `/status0/{org}/{slug}` URL derived from the
request host.

### GET /api/v1/status-pages/:org/:slug/badge
SVG badge (shields.io style) for the page's overall status — the static,
script-free sibling of the JS embed widget, for contexts like GitHub READMEs
where scripts can't run. Auth: public. Same visibility gate as the summary
above, and reuses the same `RollupPageStatus` rollup, so it can never disagree
with the summary or the full view. Reuses the per-check badges' SVG renderer
(`internal/handlers/badges`). Sets `Cache-Control: public, max-age=60` and
`Content-Type: image/svg+xml`.

Query params (same bounds as the per-check badge endpoint):
- `label` — left-side text, default the page name.
- `style` — `flat` (default) or `flat-square`.
- `minWidth` — minimum badge width in px, 0-800.
- `width` — badge width in px, 60-800 (the page badge has no bar/graph rows,
  so this behaves the same as `minWidth` — whichever is larger wins).

Right-side text/color from the rollup status: `operational` → green,
`degraded` → yellow, `down` → red, `maintenance` → blue, `unknown` → gray.
English text in v1; localization out of scope.

### GET /embed/v1/widget.js
The embeddable live status widget (spec 2026-08-08-08) — a self-contained
vanilla-JS IIFE customers paste onto their own sites:

```html
<script async src="https://<host>/embed/v1/widget.js" data-page="org/slug"></script>
```

Auth: public. Served from the embedded status0 build
(`status0res/embed/v1/widget.js`, emitted by web/status0's `build:widget`
script) with `Content-Type: application/javascript; charset=utf-8` and
`Cache-Control: public, max-age=3600`.

**`/embed/v1/` is a frozen public contract.** Once a customer has pasted the
snippet the URL and its behavior can never change; a behavior change ships as
`/embed/v2/widget.js`. Keep the v1 attribute surface minimal.

Attributes: `data-page` (required, `org/slug`), `data-mode`
(`inline` | `floating`), `data-position` (`bottom-right` | `bottom-left`,
floating only), `data-theme` (`light` | `dark` | `auto`), and per-state label
overrides `data-label-operational|degraded|down|maintenance|unknown`.

The widget polls the summary endpoint above every 60 s with an uncredentialed
`fetch` (wildcard CORS allows it; credentials would be rejected anyway) and
renders into a shadow root. A failed fetch or a 404 renders nothing. All text
reaches the DOM via `textContent` and `page.url` is scheme-validated to
http(s) before becoming an `href` — this code runs on third-party pages, so
those two invariants are guarded by
`server/internal/app/embed_widget_test.go` and
`web/status0/e2e/embed-widget.spec.ts`.

### GET /api/v1/status-pages/:org/:slug/feed.xml
RSS/Atom feed of the page's status updates. Auth: public
