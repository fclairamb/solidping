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
