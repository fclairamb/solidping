---
model: opus
effort: high
---

# Status pages have no wallboard/TV mode — a company office screen has nothing to show

## Problem

A very common deployment of a status page is a TV on an office wall: a
non-interactive, always-on screen that tells everyone at a glance whether
things are okay. SolidPing has nothing for this today, and the existing
status page is actively bad at it:

- **Designed for a reader at 40 cm, not 4 m.** The public page
  (`web/status0/src/routes/$org.$slug.tsx`) is a scrolling document —
  sections, per-resource rows, response-time charts. From across a room none
  of it is legible, and there is no dominant "we are fine / we are not"
  signal. The ambient color tokens for exactly that signal already exist
  (`web/status0/src/lib/status-style.ts` carries `bannerSurface` /
  `bannerTitle` / `bannerPill` per status) but are only used for a slim
  banner.
- **No page-level SLA number anywhere public.** The page payload
  (`ViewStatusPage`, `server/internal/handlers/statuspages/service.go`
  ~2013) serves per-resource `availability.overallAvailabilityPct` and an
  `overallStatus` rollup, but no aggregate uptime % for the page as a whole.
  The only org-wide number (`availability24h`,
  `server/internal/handlers/checks/stats.go:66`) is dash0-authenticated and
  24h-only.
- **No incident durations on the public surface.** status0's active-incidents
  component shows only a formatted `startedAt`; nothing ticks a live "ongoing
  for 43m", and resolved incidents don't show how long they took —
  even though `startedAt`/`resolvedAt` are both in the public incident shape
  (`server/internal/db/models/incident_publication.go`, public history at
  `GET /api/v1/status-pages/:org/:slug/incidents`).
- **No access model for a shared screen.** A company screen usually wants
  "unlisted but not public". Today the options are `public`, `password`
  (unlock cookie expires after 12h — `server/internal/statuspagelock/
  statuspagelock.go` — so someone re-types the password on the TV every
  morning), or `private` (404 on all public endpoints,
  `server/internal/db/models/status_page.go`). None works unattended.
- **No staleness guard.** status0 polls every 30s
  (`web/status0/src/api/hooks.ts:240`) but renders nothing when polling
  silently dies. On a wallboard, a frozen green screen during an outage is
  worse than no screen at all.

Competitive pressure is real: Hyperping ships exactly this feature
("TV mode: 60s auto-refresh, fullscreen — for office wallboards",
`wiki/competitors/hyperping/platform.md:90`), and `wiki/roadmap.md:74-75`
already lists "public SLA sections on status pages" as an open item — which
this spec partially delivers.

## Proposal

Add a **TV mode** as a display mode of an existing status page — a new
status0 route reusing the page's curation, branding, thresholds, and data
hook. No new selection mechanism for "which checks matter"; the status page
already is that.

### 1. Route & layout (status0)

New route `web/status0/src/routes/$org.$slug.tv.tsx` (and the default-page
variant `/$org/tv`; for custom-domain pages, `/tv` on the domain root —
follow how `server/internal/app/status0_meta.go` stamps the org/slug meta).
Single non-scrolling viewport, big typography, cursor hidden after a few
seconds of inactivity. Layout, top to bottom:

- **Ambient state**: the whole background tinted from the page state, paired
  with a large icon + word ("All systems operational" ✓) — color alone must
  never carry the signal (~8% of men are red-green colorblind). Reuse and
  extend the `statusStyles` map in `web/status0/src/lib/status-style.ts`.
  Mapping: `operational` → green, `degraded` → orange, `down` → red,
  `maintenance` → blue/amber (a planned window must never paint the office
  red), `unknown`/stale → grey. Additionally escalate from active incident
  publications: an active `critical` publication → red, `major`/`minor` →
  at least orange, even if checks currently pass (manually published
  incidents must show).
- **Overall SLA number**: one big percentage over the page's existing
  `historyPeriod` window (24h/7d/30d/90d), labeled with the window
  ("30-day uptime"). Only when the page's `showAvailability` setting is on.
- **Days since last incident**: computed from the most recent resolved
  publication's `resolvedAt` in the public incident history; "No incidents
  recorded" when history is empty; hidden while an incident is active.
- **Active incidents** (when any): title, severity, affected resources, and
  a **live-ticking duration** ("ongoing for 1h 12m", client-side from
  `startedAt`). If more than 2 are active, auto-cycle every ~10s rather than
  shrink.
- **Last 3 resolved incidents**: small cards — title, when, "resolved in
  23m" (client-side `resolvedAt - startedAt` from the public `/incidents`
  history; no backend change needed).
- **Footer**: page/org branding, a subtle live pulse + "updated HH:MM:SS".

Explicitly **out of scope for v1**: per-incident response-time charts on the
TV (from 4 m a chart is decoration; incident title + duration + affected
components carry the information — keep charts for a follow-up), and any
interactive element whatsoever.

Prototype/style note: prefer a *tinted dark* background variant over a
full-brightness light-green wash — a 24/7 bright screen is glaring in an
office and rough on TV panels. Decide light vs dark (or follow
`prefers-color-scheme`) during implementation; whichever is chosen must keep
the three states unmistakably distinct in both.

### 2. Page-level availability aggregate (backend)

Extend the public page payload (`ViewStatusPage` and the lighter
`ViewStatusPageSummary`, `server/internal/handlers/statuspages/service.go`)
with a page-level `overallAvailabilityPct` over the page's `historyPeriod`,
computed server-side next to the existing per-resource enrichment
(`enrichWithAvailability`). v1 semantics: the mean of the per-resource
`overallAvailabilityPct` values (matches what users can already verify
per-row); resources without data are excluded from the mean. Respect
`showAvailability` (omit the field when off) and the existing caching rules
in `server/internal/statuspagecache/statuspagecache.go`. Update the OpenAPI
spec (`server/internal/app/openapi/openapi.yaml`).

A time-weighted union ("any resource down = page down") is a defensible
alternative; it is stricter and harder to explain. Sticking with the mean is
the recommendation — note the choice in the docs.

### 3. Kiosk token (backend + dash0)

A revocable, long-lived, per-page **kiosk token** so a TV can display a
`password` or `private` page unattended:

- Model: a single token per status page, stored **hashed** on the page (or a
  small side table), minted/regenerated/revoked from dash0. Regenerating
  invalidates the old one.
- Transport: `?kiosk=<token>` accepted by the TV route and by the public
  page/summary/incidents endpoints for that page
  (`server/internal/handlers/statuspages/handler.go`). A valid token grants
  **read-only view** of the page: it bypasses the password lock, and makes a
  `private` page viewable (today `private` 404s everywhere — the token is
  what turns "private" into "unlisted" for this one screen). Invalid or
  revoked token behaves exactly as no token (401 `STATUS_PAGE_LOCKED` /
  404 respectively) — no oracle.
- Kiosk-authenticated responses must be `private, no-store` like
  password-unlocked ones (`statuspagecache`).
- The SPA should move the token out of the visible URL after load
  (`history.replaceState`) and keep it in memory for subsequent polls, so
  it isn't burned onto the screen for every passerby.
- dash0: on the status page detail
  (`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`
  area), a "TV mode" card following the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`): the copyable TV
  URL, and for non-public pages a generate/regenerate/revoke control for the
  kiosk token (show the token once at mint time, like an API key).
- Public pages need no token — their TV URL just works.

### 4. Staleness guard (status0)

- TV route polls the page payload every 30s (reuse `usePublicStatusPage`) and
  the incident history on an interval too (`usePublicIncidentHistory`
  currently has **no** `refetchInterval` — give it one on this route).
- Track the last successful poll. After ~90s without one (3 missed polls),
  flip the entire board to the grey stale state: "Data stale since HH:MM" —
  never leave a confident green up when the data is dead. Recover
  automatically on the next successful poll.

### 5. Tests

- **Backend**: page-level aggregate math (mean, no-data resources excluded,
  omitted when `showAvailability` off); kiosk token grants view of a
  password page and a private page, wrong/revoked token yields exactly the
  tokenless behavior (401/404), regenerate invalidates the old token;
  cache-control on kiosk responses is `private, no-store`.
- **E2E** (Playwright, `web/dash0/e2e/`, alongside the existing public-page
  specs): TV route renders the operational state with the SLA number; an
  active incident flips the ambient state and shows a duration; a
  password-protected page's TV URL with kiosk token renders without a
  password prompt; the stale state appears when the API is unreachable
  (route-abort the polls).
- **i18n**: new strings in all four status0 locales
  (`web/status0/src/locales/{en,fr,de,es}/`) and the dash0 locales for the
  TV-mode card; run the locale-parity unit tests.

### 6. Docs

A short page in `web/docs/` (status pages section): what TV mode is, the URL
shape, kiosk tokens for non-public pages, and the SLA-number definition.

### Open questions (decide during implementation, don't grow scope)

- Whether `/$org/tv` (default page) is worth wiring in v1 or slug-only is
  enough.
- Poll cadence: keep 30s, or tighten to 15s while an incident is active.
- Whether the kiosk token should also unlock the *normal* page route or stay
  TV-only (recommendation: TV + the JSON endpoints it needs, nothing more).

---

## Implementation Plan

### Decisions on the spec's open questions

1. **`/$org/tv` (default page)** — **wired in v1.** It is one thin route file
   delegating to the same board component, and "point the TV at the org" is the
   shape an operator will reach for first. Custom-domain root `/tv` is wired for
   the same reason (the SPA reads the server-stamped `sp-page` meta, exactly
   like `/`). Known, accepted collision: an org whose *page slug* is literally
   `tv` loses `/$org/tv` to the TV route (static segments outrank `$slug` in
   TanStack Router). Documented in the docs page.
2. **Poll cadence** — 30 s at rest, **15 s while the board is not operational**
   (any active incident, or a non-operational rollup). The staleness threshold
   stays a fixed 90 s regardless of cadence, so a tightened poll does not make
   the stale guard hair-triggered.
3. **Kiosk token scope** — **TV route + the JSON endpoints it needs, nothing
   more.** The token is accepted by the public page / summary / incidents
   endpoints (they are what the board fetches) and by the TV routes. The normal
   SPA page route never forwards it, so `/$org/$slug?kiosk=…` still shows the
   unlock form.

### Steps

1. **Migration** `017_v0_21_0.{up,down}.sql` (Postgres + SQLite):
   `status_pages.kiosk_token_hash text` + column comment.
2. **Model / DB layer**: `StatusPage.KioskTokenHash`,
   `StatusPageUpdate.KioskTokenHash`; `applyStatusPageAccessColumns` in both
   dialects writes NULL for `""` (revoke) exactly like `password_hash`.
3. **`internal/statuspagekiosk`** (new package): token generation (32 random
   bytes, base64url), `sha256` storage hash, constant-time `Valid`, the
   request-scoped `Grant` reading `?kiosk=`, and `Decide(ctx, page)` returning
   allow / not-found / locked. `Decide` is the single gate both
   `statuspages` and `incidentpublications` call, so the two can never drift.
   Invalid token ⇒ grant false ⇒ byte-identical outcome to no token.
4. **Gate wiring**: mount `statusPageKioskGrant` next to `statusPageUnlockGrant`
   on the public API surface; replace the duplicated
   `Visible`/`Allows` pairs in `statuspages.ViewStatusPage`,
   `statuspages.ViewStatusPageSummary` and
   `incidentpublications.ViewPublicIncidents` with `statuspagekiosk.Decide`.
   A disabled page stays 404 even with a valid token.
5. **Page-level `overallAvailabilityPct`**: mean of the per-resource
   `overallAvailabilityPct`, resources with no data excluded, `nil` when
   `showAvailability` is off or nothing has data. Computed from the SAME
   enriched values on the full view; the summary runs the availability half of
   the same enrichment (response time forced off) so the two cannot disagree.
   `GenerateBadge` opts out, so the badge stays cheap. OpenAPI updated.
6. **Kiosk admin endpoints**: `POST` / `DELETE`
   `/api/v1/orgs/:org/status-pages/:statusPageUid/kiosk-token` — mint (returns
   the token ONCE) and revoke. `hasKioskToken` on the admin payload only.
7. **status0 TV board**: `src/components/tv/…` + routes `$org.$slug.tv.tsx`,
   `$org.tv.tsx`, `tv.tsx`. Tinted-dark ambient surface extending
   `statusStyles` (`tvSurface` / `tvAccent` / `tvIcon`), icon + word beside
   every colour, big SLA number, days-since-last-incident, ticking active
   incident durations with auto-cycling past two, last three resolved
   incidents, footer pulse + `updated HH:MM:SS`, cursor hidden after idle,
   90 s staleness guard.
8. **Kiosk token in the SPA**: read `?kiosk=` once, keep it in memory,
   `history.replaceState` it out of the address bar, forward it on every poll.
9. **dash0 TV-mode card** on the status-page detail route: copyable TV URL,
   and for non-public pages generate / regenerate / revoke with the token shown
   once at mint time.
10. **i18n**: `tv.*` keys in all four status0 locales and `tvMode.*` in all four
    dash0 `statusPages.json`, with parity unit tests.
11. **Docs**: `web/docs/docs/features/status-page-tv-mode.md` — what it is, URL
    shapes, kiosk tokens, and the SLA-number definition (mean of per-resource
    availability over the page's history period, no-data resources excluded).
12. **Tests**: backend aggregate math + the full kiosk security matrix
    (password page, private page, wrong token, revoked token, regenerated
    token, `private, no-store`), status0 unit tests for the TV derivations, and
    Playwright coverage of the operational board, an active incident, a kiosk
    URL on a password page, and the stale state.
