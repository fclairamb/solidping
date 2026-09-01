---
model: sonnet
effort: high
---

# Stale check-stats cache keeps the empty-org onboarding hero on screen for minutes after the first check is created

## Problem

On production (`/dash0/orgs/me-me-me`), the dashboard kept showing the
"create your first check" onboarding hero for well over a minute after a
check had been created. The org was no longer empty, but the page's mode
switch didn't notice.

The onboarding hero is gated on
`isEmptyOrg = !statsQuery.isPending && stats.total === 0`
(`web/dash0/src/components/dashboard/dashboard-page.tsx:343`). The gate is
**not** driven by the checks list — it reads `GET /orgs/:org/checks/stats`
via `useCheckStats` (query key `["check-stats", org]`,
`web/dash0/src/api/hooks.ts:692`). Three layers of caching stack up to keep
that value stale after a check is created:

1. **Server: 60s cache with deliberately no invalidation.**
   `server/internal/handlers/checks/stats.go:20` caches the per-org stats
   snapshot for `defaultCheckStatsTTL = time.Minute`, and the comment states
   there is *deliberately no invalidation machinery* (spec 2026-08-02-06).
   Once a dashboard visit primes the cache with `total: 0`, every stats
   fetch for the next minute — including a full page reload — answers
   `total: 0`. The original trade-off was sized for KPI-tile staleness; it
   did not account for this endpoint also gating the 0-vs-not-0 mode switch
   of the entire dashboard page.

2. **Client: no check mutation invalidates `["check-stats", org]`.**
   `useCreateCheck` (`web/dash0/src/api/hooks.ts:806-809`) invalidates
   `["checks", org]` and `["checks", "infinite", org]` only; `useDeleteCheck`
   likewise. The onboarding hero's own post-create invalidation
   (`web/dash0/src/components/dashboard/empty-state-onboarding.tsx:99`) also
   only touches `["checks", org]`. A repo-wide grep shows nothing ever
   invalidates `check-stats` — it refreshes purely on its poll timer.

3. **The poll timer is stretched to 5 minutes while live.** The stats query
   polls at `stretchWhileLive(CHECK_POLL_MS = 30s, checksLive)`
   (`dashboard-page.tsx:282`); with the live WebSocket scope acked (the
   normal case in prod) that stretches to `LIVE_LAZY_POLL_MS = 5 min`. And
   the live-hint invalidation map (`DEFAULT_QUERY_ROOTS.checks`,
   `web/dash0/src/contexts/LiveEventsContext.tsx:132`) routes a `checks`
   hint to the `checks` / `checkAvailability` roots, never to `check-stats`
   — so a check-created event refreshes the (invisible on this page) list
   but leaves the gate query untouched.

Worst case: check created (quick-start form or MCP) → live-connected
dashboard waits up to 5 min for the next stats refetch → that fetch can
still hit a server snapshot up to 60s stale → the onboarding hero sits over
a non-empty org for several minutes. Reloading the page only bypasses
layers 2–3, not the server cache — which matches the observed
"at least a minute even after coming back".

The mirror-image bug exists too: deleting the last check leaves the normal
dashboard rendering over an empty org for the same window.

## Proposal

Fix all three layers; the server-side bust is the load-bearing one (without
it the hero still lingers up to 60s no matter what the client does).

1. **Server — bust the per-org stats cache on total-changing mutations.**
   Add an `invalidate(orgUID)` method to `checkStatsCache`
   (`server/internal/handlers/checks/stats.go`) and call it from every
   service path that changes the set of in-scope checks: check create,
   delete, and any bulk paths (apply/import/promote — enumerate the actual
   write paths in `internal/handlers/checks/service.go` and friends).
   Status transitions from the worker pipeline deliberately keep riding the
   TTL — the counters may stay up to a minute stale, only `total` crossing
   zero flips the page mode, and create/delete are rare, cheap places to
   bust. Update the "deliberately no invalidation" comment to reflect the
   new contract. Note the cache is per-process; that's fine — each API
   replica recomputes on its next miss, and the one the creating request
   hit answers correctly immediately.

2. **Client — invalidate `["check-stats", org]` alongside the checks list**
   in `useCreateCheck` and `useDeleteCheck`
   (`web/dash0/src/api/hooks.ts`). The extra invalidation in
   `empty-state-onboarding.tsx:99` can stay as-is (it becomes redundant
   with the hook doing it, but is harmless) or be simplified.

3. **Live map — refresh stats on transition-driven hints.** Add a
   `check-stats` query root under the `checks` entity's `checks` kind in
   `DEFAULT_QUERY_ROOTS` (`LiveEventsContext.tsx`), so an MCP- or
   API-created check flips the dashboard without waiting for the lazy
   5-minute poll. Do **not** attach it to the `results` kind — that's the
   firehose spec 2026-08-09-07 explicitly keeps away from org-wide
   refetches; the `checks` kind only fires on real membership/status
   transitions.

Tests to prove the fix (not just green builds):

- Server: a stats fetch primes the cache with `total: 0`; creating a check
  then makes the *next* stats fetch report `total: 1` without waiting out
  the TTL (and the symmetric delete case). A control test showing a plain
  result write does NOT bust the cache preserves the TTL trade-off.
- Frontend unit: creating a check via `useCreateCheck` invalidates
  `["check-stats", org]`; a `checks`-kind live hint invalidates it too,
  while a `results`-kind hint does not.
- E2E (existing onboarding flow): after quick-start creating the first
  check and navigating back to the dashboard, the KPI tiles render instead
  of the hero without a minute-long wait.

## Implementation Plan

### Layer 1 — server (`server/internal/handlers/checks/`)

- Add `(*checkStatsCache).invalidate(orgUID string)` to `stats.go`, next to
  `get`/`put`: locks the mutex and `delete(c.entries, orgUID)`.
- Enumerate the actual write paths in `service.go` rather than guessing:
  - `CreateCheck` and `CloneCheck` both fund creation through the single
    low-level insert helper `insertCheckResolvingSlugRace` (called only
    from those two places — grep-confirmed). Every check reaching it is
    non-internal (the `internal` field is refused on all three doors —
    create, update, upsert — per spec 2026-08-27-01), so it is exactly the
    membership-changing set. Call `s.checkStats.invalidate(orgUID)` there,
    once, right after a successful insert — this transitively covers
    `UpsertCheck`'s create branch (delegates to `CreateCheck`),
    `ImportChecks`/`importSingleCheck` (delegates to `UpsertCheck`), and
    `ApplyChecks` (delegates to `ImportChecks` for creates).
  - `DeleteCheck` has exactly one call to `s.db.DeleteCheck` (grep-confirmed
    the only call site). Call `s.checkStats.invalidate(org.UID)` right
    after it succeeds — this transitively covers `ApplyChecks`' prune loop,
    which calls `s.DeleteCheck` per pruned slug.
  - Status transitions from the worker pipeline (`checkjobsvc` et al.)
    never touch `insertCheckResolvingSlugRace` or `DeleteCheck`, so they
    correctly keep riding the TTL with no code change needed.
- Update the `defaultCheckStatsTTL` doc comment: replace "deliberately no
  invalidation machinery" with the new contract (which writes bust the
  cache, which don't, and why).

### Layer 2 — client (`web/dash0/src/api/hooks.ts`)

- In `useCreateCheck`'s `onSuccess` (~line 806) and `useDeleteCheck`'s
  `onSuccess` (~line 809), add an `invalidateQueries({ queryKey: ["check-stats", org] })`
  call alongside the existing `["checks", org]` / `["checks", "infinite", org]`
  invalidations.
- Leave `empty-state-onboarding.tsx:99`'s extra invalidation as-is — redundant
  once the hook does it, but harmless.

### Layer 3 — live map (`web/dash0/src/contexts/LiveEventsContext.tsx`)

- In `DEFAULT_QUERY_ROOTS`, under the `checks` entity's `checks` kind list
  (~line 132), add `"check-stats"` alongside the existing roots that kind
  already refreshes.
- Do **not** add it under the `results` kind — verified by reading the
  `checks` entity's kind list before editing, and asserted by a negative
  test.

### Tests

- `server/internal/handlers/checks/stats_test.go`: add
  `TestCreateCheckInvalidatesStatsCache`, `TestDeleteCheckInvalidatesStatsCache`,
  `TestCloneCheckInvalidatesStatsCache` (clone bypasses `CreateCheck` — its
  own regression proof for the shared chokepoint), and
  `TestPlainResultWriteDoesNotInvalidateStatsCache` (control: seed a check,
  cache stats, mutate the checks table directly behind the cache's back the
  same way the existing TTL test does, write a plain result row, and assert
  the next fetch still serves the pre-write cached snapshot). Uses the
  existing `newStatsService`/`seedStatsChecks` fixtures and
  `SetCheckStatsTTLForTest` with a long TTL so a pass can only mean the
  cache was actually busted, not that the TTL happened to expire.
- `web/dash0/src/api/hooks.test.ts` (or wherever `useCreateCheck`/
  `useDeleteCheck` are already covered): assert the mutation's `onSuccess`
  invalidates `["check-stats", org]`.
- `web/dash0/src/contexts/LiveEventsContext.test.tsx` (or equivalent): a
  `checks`-kind hint invalidates `["check-stats", org]`; a `results`-kind
  hint does not (the negative assertion guarding the layer-3 constraint).
- `web/dash0/e2e/`: extend the existing onboarding-flow spec so that after
  quick-start creates the first check and the test navigates back to the
  dashboard, it asserts the KPI tiles are visible and the empty-state hero
  is gone — no artificial wait for the old 60s/5min windows.
