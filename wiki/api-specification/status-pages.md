# Status Pages

Status pages, their sections and resources, subscribers (double opt-in for
email, operator-registered for webhook/Slack), status updates, brand assets,
and the public read surfaces.

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

#### Visibility, password protection and white label (spec 2026-08-21-07)

`visibility` takes three values:

| Value | Public endpoints answer | Meaning |
|---|---|---|
| `public` | the page | World-readable. |
| `private` | `404 STATUS_PAGE_NOT_FOUND` | Hidden entirely — indistinguishable from a page that does not exist. |
| `password` | `401 STATUS_PAGE_LOCKED` | Shared with a secret; unlockable (see the unlock endpoint below). |

Related fields on create/update:

| Field | Direction | Notes |
|---|---|---|
| `password` | write-only | Required when switching to `password` unless one is already stored. Empty string clears it (refused while visibility is still `password`). Minimum 6 characters. Never echoed. |
| `hasPassword` | read-only | Whether a password is stored. The hash is never serialized. |
| `hideBranding` | read/write | The page's white-label OPT-IN. On admin payloads this is the raw stored value; on the PUBLIC payload it is already ANDed with the org's `whiteLabel` entitlement. |
| `whiteLabelAllowed` | read-only, **admin only** | Whether the org holds the `whiteLabel` entitlement. Never present on a public payload — plan state is not public. |
| `logoUrl` / `faviconUrl` | read-only | Public paths of the uploaded brand assets, or absent. Derived from the file UIDs in `settings -> branding`; set by the asset endpoints below, never by PATCH. |

## Brand assets

Per-page logo and favicon (specs 2026-08-21-07, 2026-08-22-03). Uploads are
`multipart/form-data` with a single file part named after the slot.

The three knobs are stored in `status_pages.settings -> branding`, not in
columns of their own; `logoUrl` / `faviconUrl` / `hideBranding` are unchanged on
the wire.

The blobs are served from an unsigned public route authorized by the file's
**attachment topic** (`status-pages/<uid>/logo` or `/favicon`) and by the file
row still being live. Replacing or clearing an asset soft-deletes the file and
takes it offline immediately, and deleting the page reaps the whole
`status-pages/<uid>/` prefix.

**Disabling a page does NOT take its assets offline**, and neither does making
it `private` or `password`. That is a deliberate loosening: the URL embeds an
unguessable UUIDv4 and a brand logo is not a secret (on a password page it is
the image shown above the prompt). To un-publish a logo, clear it.

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/logo
Upload the page logo (part name `logo`). PNG/JPEG/WebP/GIF/SVG, max 1 MB.
Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/logo
Clear the page logo. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/favicon
Upload the page favicon (part name `favicon`). ICO/PNG/WebP/GIF/SVG, max 256 KB.
JPEG is deliberately not accepted for a favicon. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/favicon
Clear the page favicon. Auth: required

### GET /pub/status-page-assets/:uid
Serve an uploaded asset. Auth: **public, unsigned**. `404` once the file is
replaced, cleared, or reaped with its page. Served with `nosniff`; SVG is served
as an attachment so it can never execute as a document on the origin.

This path and `/pub/assets/:uid` are the **same handler and the same check** —
`/pub/status-page-assets/` is the URL shape `logoUrl` / `faviconUrl` emit, kept
byte-identical because spec 2026-08-22-03 is a storage-and-authorization
refactor rather than a URL change.

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

A subscription has a `channel`: `email`, `webhook` or `slack`.

**Email** subscription is **double opt-in**: anyone can request one from the
public status page, but nothing is delivered until the emailed confirmation
link is followed. Double opt-in is the primary anti-abuse control, which is why
the public subscribe route exists at all while listing and deleting are authed.

**Webhook and Slack** subscriptions are **operator-side only** (spec
2026-08-21-07). A visitor pasting an incoming-webhook URL has no verification
story, and the URL is itself a credential — so they are registered through the
authenticated route below and are created already confirmed. Public self-serve
stays email-only, and the public subscribe form says so.

The endpoint URL is treated with the same opacity as a subscriber email and
then some: it is stored **encrypted** (per-org DEK, `internal/crypto/credentials`)
and list responses return only a masked `endpoint` hint — never the real URL.

Webhook deliveries carry the `internal/servicesig` HMAC headers
(`X-SP-Signature: v1,<b64>`, `X-SP-Timestamp`, `X-SP-Key-Id: status-page-subscriber`)
signed with the subscription's own secret. After 5 CONSECUTIVE delivery
failures the subscription is disabled and a `statuspage.subscriber.disabled`
event is recorded, so an operator can see that deliveries stopped and why. One
success resets the counter.

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers
List the subscribers of a status page. Auth: required

Each row carries `channel`, the masked `endpoint` (endpoint channels only),
`failureCount` and `disabled`.

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers
Request an email subscription. Auth: **public**. Body: `{ "email": "…" }`.
Sends a confirmation email; creates nothing deliverable until confirmed.
Returns `202`. A `channel` other than `email` is refused with
`VALIDATION_ERROR`.

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers/endpoints
Register a webhook/Slack delivery. Auth: **required**. Body:
`{ "channel": "webhook" | "slack", "url": "https://…", "signingSecret": "…" }`.
The URL must be `https` and must not resolve to a loopback/private host; a
`slack` row must point at `hooks.slack.com`. Returns `201`.

`signingSecret` is optional. Supply your own when the receiver already verifies
a known secret; omit it and one is generated. **Either way the create response
carries it once**, as `data.signingSecret` — it is never stored in a readable
column and never returned again, so a receiver that loses it needs the
subscription re-created. Without that one-time disclosure the HMAC on every
delivery would be unverifiable, which is worse than not signing at all.

The separate path is not cosmetic. Both routes live on one chi mux, and chi
silently OVERWRITES a duplicate method+pattern instead of panicking — the two
handlers cannot share `…/subscribers`, or the last registration wins and the
other becomes unreachable. Pinned by `TestStatusSubscriberRoutesDoNotCollide`.

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

A disabled page, a `private` page, and a page that doesn't exist all return an
identical `NOT_FOUND` — none of these routes leak page existence.

A `password` page is different on purpose: it answers `401` with code
`STATUS_PAGE_LOCKED` on every read below (page, default page, summary, badge,
incidents, `feed.xml`, and the public subscribe) until the caller presents a
valid unlock cookie. Being knowable-but-locked is the whole point of the mode;
`private` remains the option for "pretend it does not exist".

### POST /api/v1/status-pages/:org/:slug/unlock
### POST /api/v1/status-pages/:org/unlock
Unlock a password-protected page (the second form targets the org's default
page). Auth: **public** — the password IS the credential. Body:
`{ "password": "…" }`.

On success: `204`, plus a `Set-Cookie: sp_unlock_<pageUid>` that is
**host-only** (no `Domain` attribute), `HttpOnly`, `SameSite=Lax`, `Secure` when
the request arrived over TLS, and valid for 12 hours. Host-only is what makes
it work on a custom domain without minting a cookie for `solidping.io`.

Wrong password: `401 STATUS_PAGE_LOCKED`. Attempts are rate-limited per
(client IP, page) — 10 per 5 minutes, then `429 RATE_LIMITED`.

The cookie is signed with a key derived from the stored password hash, so
**changing or clearing the password invalidates every outstanding unlock
immediately**, with no revocation list.

Already-confirmed subscribers of a password page keep receiving notifications:
only page *views* and the subscribe *form* are gated, never delivery.

### GET /api/v1/status-pages/:org
View the default status page for an organization. Auth: public. Same payload
shape as `GET /api/v1/status-pages/:org/:slug`, resolved to the org's default
page.

### GET /api/v1/status-pages/:org/:slug
View a specific status page by slug. Auth: public. Full payload: sections,
per-resource live status, and (when enabled) availability/response-time
history. Also carries `overallStatus` and `statusCounts` — the page-level
rollup computed server-side (see below). Sets `Cache-Control` per the shared
visibility rule described in **Caching on the public surface** below.

Each `responseTimeSeries[].points[]` entry carries, alongside `time`,
`durationP95` and the probe's own `status`, the **availability** of the slice
it covers (spec 2026-08-26-10): `availabilityPct` (null when the row has no
countable probe — no data is not 100%), `totalChecks`, `successfulChecks`, and
`availabilityStatus` in `up|degraded|down|noData`. The status is resolved
server-side against the page's own `availabilityThresholds` with the shared
classifier (`uptimebar.Classify`, small-bucket guard included), so the strip
under the response-time chart and the availability bar above it can never paint
the same numbers differently. Lifecycle markers and abandoned attempts are
excluded from both numerator and denominator; warning counts as up; maintenance
probes count like any other.

### GET /api/v1/status-pages/:org/:slug/summary
Lightweight "is it up?" companion to the full view above. Auth: public. Same
visibility gate, and the rollup comes from the exact same live data via the
shared `RollupPageStatus` helper, so it can never disagree with the full view
or the SVG badge. Sets `Cache-Control` per the shared visibility rule
(**Caching on the public surface**, below).

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

### Caching on the public surface

Every public read of a status page — the page view, the summary, the SVG
badge, the public incident history, the Atom feed and both status0 SPA shells
(path-based and custom-domain) — derives its `Cache-Control` from one helper,
`internal/statuspagecache`:

| page visibility | `Cache-Control` |
|---|---|
| `public` | `public, max-age=60` (`max-age=300` on the feed) |
| `password` | `private, no-store` |
| `private` | `private, no-store` |
| 401 / 404 answers | `private, no-store` |

Anything that is not exactly `public` is gated, so a visibility added later
arrives locked rather than world-cacheable. **An unlock cookie does not make a
page cacheable**: it authorizes that visitor, not the CDN or corporate proxy in
front of them, so an unlocked `password` page is still `private, no-store`.
The gated 401/404 stops a shared cache from turning the public surface's error
replies into a map of which pages exist.

`Vary` differs between the two branches, on purpose:

- **public** → `Vary: X-Forwarded-Proto` only. That header genuinely changes the
  body (the summary's absolute `page.url` and the shell's `og:url` take their
  scheme from it). `Cookie` is deliberately **not** listed: a public page's body
  does not depend on any cookie, and Cloudflare, Fastly and Varnish all refuse
  to cache a response carrying `Vary: Cookie` — listing it would quietly undo
  the shared-cache win this change exists to buy. `Vary` is also the wrong tool
  for a `public → password` flip: it does not invalidate anything at the origin,
  so what bounds that window is `max-age`, not `Vary`.
- **gated** → `Vary: Cookie, X-Forwarded-Proto`, where the unlock cookie really
  does decide whether the page renders or 401s. Belt and braces next to
  `no-store`, but the two travel together so relaxing the directive later cannot
  silently drop it.

`Accept-Language` is absent from both: a page renders in the language stored on
the page row, not the one the browser asks for.

The path-based shell (`/status0/...`) is the one surface that stays
unconditionally `public, max-age=60`, and it is safe: `status0MetaForPath`
resolves the page **without** installing the request's unlock grant, so
`statuspagelock.Allows` denies by default and a gated page's name or
description is never injected into it. Its custom-domain twin injects
unconditionally — there the host *is* the page — so that one follows visibility.

Deliberately out of scope: `ETag`/conditional requests (revalidation would
still have to compute the body to hash it), server-side response caching and
`stale-while-revalidate`.

### GET /api/v1/status-pages/:org/:slug/badge
SVG badge (shields.io style) for the page's overall status — the static,
script-free sibling of the JS embed widget, for contexts like GitHub READMEs
where scripts can't run. Auth: public. Same visibility gate as the summary
above, and reuses the same `RollupPageStatus` rollup, so it can never disagree
with the summary or the full view. Reuses the per-check badges' SVG renderer
(`internal/handlers/badges`). Sets `Content-Type: image/svg+xml` and
`Cache-Control` per the shared visibility rule (**Caching on the public
surface**, below).

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
