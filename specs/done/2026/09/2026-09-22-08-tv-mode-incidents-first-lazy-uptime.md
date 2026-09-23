---
model: opus
effort: high
---

# TV mode waits 7 s for a payload it barely reads: incidents first, the uptime number last

## Problem

The wallboard (`tv-route.tsx`, `tv-board.tsx`, spec `2026-08-29-08`) shows
`loading` until the full page payload lands, then everything at once. On the
dev deployment's 200-resource page that payload is 3.2 MB and takes 6.6–7.4 s
server-side (measured, see spec `2026-09-22-05`); in the browser the shell
and the JS bundle are done at 214 ms and the board appears at 7.4 s on a
fast link, 12 s on an office one. Meanwhile the incident history — the one
thing the room actually needs to see — answers in 10 ms and is already in
hand.

The board renders one thing from the expensive part of the payload: the
page-level uptime percentage. It is a 7- or 90-day mean. It does not move
between two polls, and nobody in front of a wall screen acts on its second
decimal. It should not be on the critical path of the first paint, and it
should not be recomputed every 30 s.

## Decision

Three changes to how the TV route loads, in priority order of what the room
needs:

1. **Incidents are the primary read.** The history query starts at mount, and
   the board renders as soon as it lands — before the page payload — with
   what the incidents alone can say: the headline state (from the severity
   floor), the active incidents panel, the resolved strip, "days since the
   last incident". The page fills in the rest when it arrives.
2. **The page read asks for nothing expensive.** `include=` (spec
   `2026-09-22-07`), so the poll carries the name, the rollup, the counts and
   the per-resource statuses only: 64 KB instead of 3.2 MB, and none of the
   history queries on the server.
3. **The uptime number is loaded last, from the summary endpoint, on its own
   cadence.** It is requested only once the board is on screen, refreshed
   every five minutes, and never blanks while a refresh is in flight.

## Design

### Queries

| Query | Endpoint | Starts | Cadence |
|---|---|---|---|
| incidents | `GET …/status-pages/{org}/{slug}/incidents` | mount (slug route) / as soon as the slug is known (default-page route) | 30 s, 15 s while non-green (unchanged) |
| page | `GET …/status-pages/{org}[/{slug}]?include=` | mount | 30 s / 15 s (unchanged) |
| uptime | `GET …/status-pages/{org}/{slug}/summary` | once `page` is in hand and `page.showAvailability` | 5 min; `placeholderData: keepPreviousData` |

- **Active incidents come from the history endpoint**, derived as `state !==
  "resolved"`, and the board stops reading `page.activeIncidents`. One source,
  the fast one, feeds both the severity floor (`resolveTvState`) and the
  panel; the board can no longer show an incident in the panel that the
  headline does not know about because the two payloads were fetched at
  different instants. Both endpoints are served by the same
  `ListPublicIncidents` with the `active` flag as the only difference, so the
  objects are the same shape.
- The summary's `overallAvailabilityPct` is the same mean the page computed
  (`summaryAvailability` runs the same enrichment with response time forced
  off — the comment there says so, deliberately). The window label keeps
  coming from `page.historyPeriod`.
- The uptime query is kiosk-gated like the page (the kiosk grant middleware
  sits on the whole public group), so `withKiosk` applies to it too.
- Add `usePublicStatusPageSummary(org, slug, {kioskToken, enabled,
  refetchInterval})` to `hooks.ts`, with its own query key family.

### Rendering states

`TvBoard` takes `page?: StatusPage` (optional) and `incidents?:
PublicIncident[]`. The route renders:

- **Nothing yet**: the existing `loading` shell.
- **Incidents in, page pending**: the board, with `state =
  resolveTvState(undefined, active)`, the headline copy for that state, the
  active-incidents panel or the resolved strip, "days since". The page name
  slot, the status counts, the failing-resources fallback and the uptime tile
  are empty. No new copy is needed: an empty slot is an empty slot, and the
  board's own doctrine is "never claim more than it knows".
- **Page in**: as today, minus the tile.
- **Uptime in**: the tile appears. Until then, and whenever the summary
  omits the number, the tile is absent (`tv-availability` not rendered).
- **Locked / not found**: a `401` or `404` on **either** the page or the
  incidents query takes over the whole screen exactly as the page query's
  errors do today; the two endpoints share one visibility gate, so they never
  disagree.
- **Stale**: still driven by the page query's `dataUpdatedAt`. A slow or
  failed summary never greys the board; a stale board hides the tile as it
  hides every other confident number.

### Cadence

`pollIntervalMs` is unchanged for the page and the incidents. The summary
polls every 5 minutes regardless of state: the mean cannot move enough in
five minutes to matter, and during an incident the number is not what the
room is looking at.

### Default-page route

`/{org}/tv` has no slug in the URL and there is no slug-free incidents or
summary route. It resolves the slug from the page response as today; with the
page now answering in about a second that is acceptable. Adding
`/status-pages/{org}/incidents` and `/status-pages/{org}/summary` is a
possible follow-up; note the routing hazard that `/status-pages/:org/:slug`
would already match `/status-pages/{org}/summary` with `slug=summary`.

## Tests

`web/status0/e2e/tv-board.spec.ts` mocks
`**/api/v1/status-pages/${ORG}/${SLUG}` with an exact glob; the page URL now
ends in `?include=`, so the mock must match on `url.pathname` (a predicate)
or on `…/${SLUG}?*`. Every existing test in the file stays, and the `mock()`
helper gains a summary route. New coverage:

1. The page request URL carries `include=` empty; the mocked page body
   without any `availability` renders the board (no `tv-availability`).
2. **Incidents first**: delay the page mock by 3 s, fulfil the incidents at
   once → within 500 ms the active incident title is visible and
   `data-tv-state` reflects its severity; after the delay the page name and
   counts appear without a remount (the incident element keeps its handle).
3. **Uptime last**: delay the summary → the board is on screen without the
   tile; the tile appears with the summary's number and the page's window
   label; a later summary that omits the number removes the tile.
4. A `401` on the incidents mock with a `200` page shows the locked screen.
5. The summary is not requested for a page with `showAvailability=false`.
6. Unit tests in `src/lib/tv-board.test.ts` for the new pure helper that
   derives active incidents from history (state filter, ordering preserved).

Backend: none beyond spec `2026-09-22-07`; the summary endpoint is unchanged.

## Docs

- `web/docs/docs/features/status-page-tv-mode.md`: the uptime bullet
  ("shown only when the page publishes availability") gains "loaded after the
  board, refreshed every five minutes"; the "refreshes every 30 seconds"
  paragraph names the three reads and their cadences; a sentence that the
  board shows incidents before the rest of the page has loaded.
- `wiki/features/status-pages.md` if it describes the TV data flow.
- Changelog entry (user-visible: the wallboard shows incidents within a
  second instead of waiting for the full page).

## Acceptance

On the dev deployment's 200-resource page, in the browser's resource timing:
the incidents response and the first board paint under 1 s from navigation;
the page request under 100 KB uncompressed; the summary request starting
after the board is painted. `make test-dash` and the status0 e2e suite green.

## Out of scope

- Batching the 400 per-resource lookups in `enrichResourceInfo` (still the
  bulk of the page request's remaining ~1 s). Separate spec.
- Slug-free incidents / summary routes.
- Any change to the ordinary status page.

## Implementation Plan

Verified before writing any code: the backend needs nothing. `ListPublicIncidents`
applies no state filter when `activeOnly=false`, so the history endpoint already
returns the open publications alongside the resolved ones, in the same
`PublicIncident` shape; `ViewPublicIncidents` routes 401 `STATUS_PAGE_LOCKED` and
404 through the same `handlePublicError` the page view uses; and both
`/status-pages/:org/:slug/incidents` and `/status-pages/:org/:slug/summary` sit on
the same `publicOrgAPI` group the `statusPageKioskGrant` middleware wraps, so a
kiosk token works on all three reads.

1. **`hooks.ts`** — add the `StatusPageSummary` wire type (mirroring the
   handler's `StatusPageSummaryResponse`: `status`, `counts`, optional
   `overallAvailabilityPct`, `page{name,slug,url}`, `generatedAt`) and
   `usePublicStatusPageSummary(org, slug, options)` with its own
   `["public-status-page-summary", org, slug, kioskToken]` key family,
   `withKiosk`, an `enabled` knob on top of the `!!org && !!slug` guard, and
   `placeholderData: keepPreviousData`.
2. **`lib/tv-board.ts`** — add the pure `activeIncidents(incidents)` helper
   (`state !== "resolved"`, source order preserved) and `SUMMARY_POLL_MS`.
3. **`tv-route.tsx`** — derive the active list from the history query and feed
   it to `resolveTvState` for the cadence; start the summary query once the page
   is in hand and publishes availability; widen the locked gate to either query;
   render the board as soon as EITHER the page or the incidents have landed.
4. **`tv-board.tsx`** — take `page?: StatusPage` and a new `availabilityPct`
   prop, stop reading `page.activeIncidents`, suppress the tile while stale, and
   suppress the headline-cause attribution when there is no rollup to attribute
   against.
5. **Unit tests** — `lib/tv-board.test.ts` for `activeIncidents`.
6. **E2E** — `e2e/tv-board.spec.ts`: `mock()` gains a summary route, folds the
   page overrides' `activeIncidents` into the history body (which is what the
   real endpoint does), and gains delay/status knobs; then the five new cases.
7. **Docs** — `web/docs/docs/features/status-page-tv-mode.md`. There is no
   `wiki/features/status-pages.md` in this tree, so nothing to update there.
8. **Gates** — `make build-status0`, `bun run lint`, `bun run typecheck:e2e`,
   `bun test ./src`, and the whole `tv-board.spec.ts` under Playwright.
