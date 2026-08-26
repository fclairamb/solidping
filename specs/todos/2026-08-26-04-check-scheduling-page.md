---
model: opus
effort: high
---

# A scheduling page to bring an org back under its checks/min limit

## Problem

When an org is over its `MaxChecksPerMinute` entitlement, the only remedies
are editing checks one by one (each period edit is a full check-form round
trip) or upgrading. There is no place that shows *where the demand comes
from* — which checks, at which periods, across how many regions — or that
helps redistribute it. The over-limit banner (spec 2026-08-26-03) needs a
destination.

The math the page surfaces: demand = Σ over enabled, non-deleted, active
(non-passive) checks of `regions × (60 / period_seconds)`; each selected
region runs the check at the full period (spec 2026-07-20-05). Passive
checks (heartbeat/email) never consume rate budget
(`server/internal/checkworker/worker.go:904` returns before the token gate)
and are excluded.

## Proposal

A dedicated dash0 page, e.g. `/orgs/$org/checks/scheduling` (implementer
picks the exact route; it should be reachable from the over-limit banner and
from the checks list). This page *is* the dedicated editing surface for one
field across many checks — the "editing navigates to a dedicated route"
convention is satisfied by the page itself; per-row period edits are inline
by design and must not open modals.

### Content

1. **Header summary**: org demand vs cap (`checksPerMinute.demand` /
   `.limit` from spec 03's API), a progress-style meter, amber when over.
2. **One row per active check**: name (linking to the check), **period**
   as an inline select (same steps as `buildIntervalOptions`,
   `web/dash0/src/components/shared/check-form.tsx:212–231`, honoring
   per-type min/max constraints; a non-standard current period appears as
   the first option per spec 2026-08-26-05), **regions only when the check
   has more than one**, the check's per-minute contribution
   (`regions × 60/period`), and its enabled state (toggling is the other
   honest lever to get under the cap).
3. **Live recalculation**: edits update the header total immediately,
   before anything is saved. Nothing writes until the user applies.
4. **Auto-rebalance**: one button that proposes new periods bringing the
   org under the cap — proportionally stretch periods (largest
   contributors first), snapping to the known steps, deterministic
   (same input ⇒ same proposal). Show the proposal as a diff (per row:
   old period → new period, old/new totals) that the user can adjust,
   then **Apply** batches the changes (`PATCH` per check via the existing
   endpoint). Client-side computation is fine; no new backend endpoint is
   required beyond spec 03's demand payload.
5. Passive checks: not listed in the main table (they cost nothing); a
   one-line note explains why, so users don't hunt for missing checks.
6. Fully usable on mobile; every primitive from the design reference
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) — add any
   missing primitive to the reference as part of the change.

### Tests

- Unit: contribution/demand math (multi-region, passive exclusion), the
  auto-rebalance proposal (deterministic, lands under cap, snaps to
  steps, respects per-type period constraints).
- Playwright: load the page, edit one period inline, see the total move,
  apply, verify the check was PATCHed; auto-rebalance proposal renders
  and applies.
- All four locales.

## Non-goals — read before implementing

- **No per-check second-offset / phase editing, manual or automatic.**
  The rate limiter is a continuously-refilling token bucket
  (`cap/60` tokens per second — `internal/entitlements/service.go`), so
  redistributing arrival seconds within the minute changes nothing about
  what it admits; and execution phases are deliberately a deterministic
  UID hash reproducible across processes
  (`internal/checkworker/scheduling/phase.go:48–57`, spec 2026-07-20-05).
  With spec 2026-08-26-02's rotation, offsets no longer decide who gets
  skipped either. If probe-smoothing phase control is ever wanted, it is
  its own spec; do not sneak it in here.
- No region editing on this page (period and enabled only); regions are
  display-only context.
- No plan upsell logic beyond linking to the existing upgrade CTA.

## Implementation Plan

Written after reading the spec, `web/dash0/CLAUDE.md`, the design reference and
spec 03's shipped payload. Frontend-only: spec 03 already computes and serves
`checksPerMinute.{demand,limit,skippedToday}`, and `PATCH /orgs/:org/checks/:uid`
already accepts `{period}` / `{enabled}` — **no Go changes**.

### 1. Pure math module — `web/dash0/src/lib/check-scheduling.ts`

Everything the page decides, decided in a testable place with no React in it:

- `PERIOD_STEP_SECONDS` — the same ladder `buildIntervalOptions` walks
  (5s … 30d). Single source for "known step".
- `isPassiveCheckType(type)` — mirrors `checkerdef.CheckType.IsPassive()`
  (heartbeat, email). Duplicated across the wire on purpose, with a comment
  pointing at the Go definition.
- `contributionPerMinute(regionCount, periodSeconds)` —
  `max(1, regions) × 60 / period`, **identical** to
  `entitlements.checksPerMinuteRate`. Zero/negative period contributes 0.
- `schedulingRowsFromChecks(checks, constraintsFor)` — keeps enabled-or-not but
  **non-passive, non-internal** checks; passive ones are counted separately so
  the page can say how many it hid.
- `totalDemand(rows)` — sums contributions of rows whose draft `enabled` is true.
- `periodOptionsFor(row)` — steps within the type's `[min,max]`, plus the
  current period prepended as a `custom` option when it is not a known step
  (local to this page; spec 05 owns the check form's own dropdown).
- `proposeRebalance({rows, limit})` — deterministic greedy: repeatedly stretch
  the single largest contributor that still has a longer step available
  (ties broken by uid, ascending), one step at a time, until total ≤ limit or
  nothing can stretch further. Returns `{ proposals: Map<uid, seconds>,
  totalAfter, reachedLimit }`. Same input ⇒ same output, by construction:
  no randomness, no iteration over unordered maps.

### 2. Shared meter — `web/dash0/src/components/shared/check-rate-meter.tsx`

`CheckRateMeter` — demand vs cap on a `Progress` bar with the "now → after"
delta when a draft is dirty, amber when over cap. New shared primitive, so it
gets an entry in `design-reference.tsx` (mandatory per CLAUDE.md).

### 3. Page — `web/dash0/src/routes/orgs/$org/checks/…` → `checks.scheduling.tsx`

Route `/orgs/$org/checks/scheduling` (static segment beats `checks.$checkUid`,
same as the shipped `checks.new.tsx`).

- Loads every check with `useInfiniteChecks(org, { limit: 100 })`, auto-paging
  until `hasNextPage` is false — a page whose whole point is the org total may
  not stop at the 100-row clamp.
- Loads `useEntitlements(org)` for `checksPerMinute` (limit + skippedToday) and
  `useCheckTypes(org)` for per-type period min/max.
- Header: `PageHeader` + `CheckRateMeter` + the amber `CheckRateLimitBanner`.
- Body: card-wrapped `Table` (design reference "List surface"). Per row: name
  (link to the check), type badge, **regions only when > 1**, inline period
  `Select`, contribution, `Switch` for enabled. Secondary columns are
  `hidden sm:table-cell`; the row stays usable at 375px.
- Draft state is local `useState` only — **no URL search params** (a layout-route
  search param drops on cold deep-link, and there is nothing here worth
  bookmarking).
- Live recalculation: the meter reads the draft, so a select change moves the
  total before anything is written.
- Auto-rebalance: a button that fills the draft from `proposeRebalance`. Changed
  rows render `old → new` inline (the per-row diff), the meter shows
  `now → after` (the totals diff), and the user can keep editing any select
  afterwards. `Apply` PATCHes only dirty rows; `Reset` drops the draft.
- Passive note: one line under the table with the hidden count and why.
- 401/403 handling comes from the shared `apiFetch` + route guard; nothing
  page-specific.

### 4. Wiring

- `check-rate-limit-banner.tsx`: retarget the `TODO(spec 2026-08-26-04)` link at
  the new page (keep the `showUsageLink` prop name and test id so no caller or
  test breaks; the label changes to "Review scheduling").
- `checks.index.tsx`: a `CalendarClock` "Scheduling" action in the page header.
- New API mutation `useApplyCheckSchedule(org)` in `src/api/hooks.ts` — one
  PATCH per dirty check, sequential, reporting per-check failures rather than
  aborting silently.

### 5. Locales

`scheduling.*` block in `src/locales/{en,fr,de,es}/checks.json`, genuinely
translated (the locale test asserts the four titles differ).

### 6. Tests

- `src/lib/check-scheduling.test.ts` — contribution math (multi-region, zero
  regions = 1, passive exclusion), demand totals, `periodOptionsFor` custom
  entry and min/max clamping, and `proposeRebalance`: determinism (same input
  twice, and a shuffled input), lands under cap, snaps to steps, never violates
  a per-type max, and reports `reachedLimit: false` when it cannot get there.
  Plus the four-locale key/placeholder completeness test.
- `e2e/check-scheduling.spec.ts` — load the page, change a period inline, assert
  the header total moves before saving, Apply and assert the PATCH, then
  auto-rebalance renders a proposal and applies it.

### Non-goals honored

No second-offset/phase editing of any kind, no region editing (display only),
no upsell logic beyond the existing upgrade CTA.
