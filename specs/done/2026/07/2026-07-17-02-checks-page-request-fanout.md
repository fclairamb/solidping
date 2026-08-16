---
model: opus
effort: high
---

# The checks page fans out one live-invalidated query per group, sustaining ~200 req/min per tab — which is why the rate limits had to be raised

## Problem

`web/dash0/src/routes/orgs/$org/checks.index.tsx` renders one
`CheckGroupSection` per group (`checks.index.tsx:869`) plus one
`UngroupedChecksSection` (`checks.index.tsx:893`). Each section owns its own
`useInfiniteChecks` query filtered to a single group
(`checks.index.tsx:330` — `?with=last_result&checkGroupUid=<uid>&limit=20`;
`checks.index.tsx:489` for the ungrouped variant with `checkGroupUid=none`).

`LiveEventsContext` invalidates **all** of them on every hint tick.
`DEFAULT_QUERY_ROOTS` maps both the `checks` and `results` kinds to
`infiniteOrgRoot("checks")` (`LiveEventsContext.tsx:111,118-124`), whose
predicate matches on `key[0] === "checks" && key[1] === "infinite" &&
key[2] === org` (`LiveEventsContext.tsx:74-79`) — the per-group options object
sits at `key[3]` and is never examined. So one hint invalidates every group's
query at once.

The damper (`LIVE_INVALIDATE_MIN_INTERVAL_MS = 3_000`,
`LiveEventsContext.tsx:45`) caps the *tick rate*, not the *fan-out*: a busy
org emits a `results` hint on every server flush (~1s), so each tab settles at
one invalidation burst every 3s = 20 bursts/min, each firing N+1 requests.

**Measured cost: ~(N+1) × 20 requests/min per open tab.** A HAR capture of the
acmetech checks page (10 groups) showed ~200 req/min. This is what forced
`a0b16df0` (`requests_per_minute` 300 → 1800, `burst` 60 → 360,
`max_concurrent` 20 → 40) — the commit message says so explicitly: *"standard
use consumed most of the old 300/min per-client budget"*. The limits were sized
to fit the fan-out rather than the fan-out being fixed.

Two aggravating details found while scoping this:

- **Collapsing a group does not stop its query.** `useInfiniteChecks` is called
  unconditionally at the top of `CheckGroupSection` (`checks.index.tsx:330`);
  `collapsed` only gates rendering (`checks.index.tsx:422`). A user who
  collapses every group still pays the full request cost.
- **429s are retried blind.** `main.tsx:57-69` never retries 4xx (`status < 500`
  → `return false`), so a 429 fails the query outright and the *next* 3s
  invalidation re-fires it — a self-sustaining ~20/min retry pulse per query
  while the bucket is empty. `apiFetch` already parses `Retry-After` into
  `ApiError.retryAfter` (`client.ts:215-226`) and nothing consumes it.

## Proposal

### Primary: (a) one batched query, grouped client-side

Replace the N+1 per-group queries with a **single** `useInfiniteChecks(org,
{with: "last_result", limit: 100})` owned by `ChecksIndexPage` — no
`checkGroupUid` filter — and bucket the results by `check.checkGroupUid` in the
page component. `CheckGroupSection` / `UngroupedChecksSection` become
presentational: they receive their `checks[]` as a prop instead of running a
query.

Request math per tab, steady state: **20 req/min regardless of group count**
(vs 220 for 10 groups). Rows/min actually drops too — 20 × 100 = 2 000 vs
220 × 20 = 4 400 — so this is a win on payload as well as on request count and
connection concurrency (the `max_concurrent` 20 → 40 bump exists because a cold
load fires every panel query at once).

`limit: 100` is the server cap (`server/internal/handlers/checks/handler.go:176-178`
clamps anything higher), so no backend change is needed.

Consequences to handle explicitly:

- **Pagination semantics change** from per-group infinite scroll to one
  page-level infinite scroll. Move the `IntersectionObserver` sentinel
  (`checks.index.tsx:347-365`) to the page level, below the last section.
- **Group count badges stay correct** — they already come from
  `group.checkCount` via `useCheckGroups` (`checks.index.tsx:380`), not from
  the paginated rows, so a partially-loaded group still shows its true total.
- **Group ordering** keeps coming from the groups list (`sortOrder`); client-side
  bucketing is order-independent.
- **Filters** (`q`, `internal`, `labels`, `status`) apply page-wide exactly as
  today, but are now serialized into one query key instead of N.
- **Existing invalidation call sites stay compatible** — `handleRefresh`
  (`checks.index.tsx:722`) and the change-group flow (`checks.index.tsx:1099`)
  both invalidate `["checks", "infinite", org]`, which still matches.

### Secondary: (c) honor `Retry-After` on 429

Independent of (a), and worth doing regardless — it converts overload from an
amplifier into a backpressure signal. In `main.tsx:57-69`, special-case 429
ahead of the `status < 500` bail-out: retry it, and have `retryDelay`
(`main.tsx:68-69`) return `error.retryAfter * 1000` when present, falling back
to the existing exponential backoff otherwise. Cap the honored delay
(e.g. 60s) so a hostile/absurd header can't wedge a query forever.

### Rejected: (b) scope hint invalidations by group uid

**Not viable without a server change.** The collection hint carries no uid *by
design*: `collectionEntityForKind` (`server/internal/realtime/hub.go:235-241`)
routes a kind to a bare `Entity`, and `dispatch`/`offerHint`
(`hub.go:258-291`) match collection subscribers on entity alone.
`hub_test.go:180` pins the resulting frame as
`{Scope: {Entity: EntityChecks}, Kinds: [KindResults]}` — no uid. The client
side agrees: `onUpdate` passes `msg.uid` through (`live-socket.ts:429`) but the
server never sets it for a collection scope.

The `Hint` *does* carry `CheckUids`, but only for routing to `check`-scoped
subscribers (`checkAttributableKinds`, `hub.go:243-251`) — and those are
**check** uids, not **group** uids. Making (b) work would need: a server change
to surface check uids on the collection update frame, plus a client-side
check→group map to translate them, plus a fallback for the `["*"]` wildcard
(`hub.go:271`) which would invalidate everything anyway. That is a larger and
more fragile change than (a), for a strictly worse result — (a) removes the
fan-out entirely rather than narrowing it.

### Constraints

- **Keep the 3s damper** (`LIVE_INVALIDATE_MIN_INTERVAL_MS`) as-is. This spec
  reduces the work *per* tick, not the tick rate.
- Do **not** revert `a0b16df0`. The raised limits become headroom rather than a
  requirement; re-tuning them down is a separate decision with its own
  regression tests (`server/internal/...` rate-limit tests pin the current
  values and would need updating in lockstep).
- Respect `web/dash0/CLAUDE.md` — start from the design reference
  (`src/routes/orgs/$org/design-reference.tsx`), keep the page fully usable on
  mobile, and keep delete actions red + `Trash2`.

### Verification

The e2e must prove the *negative* (the fan-out is gone), not just that the page
still renders. Positive control required: the new assertion must fail against
the current code.

- Add a spec in `web/dash0/e2e/` that seeds an org with several groups, counts
  `GET /api/v1/orgs/*/checks*` requests over a fixed window via
  `page.on("request")`, and asserts the count does not scale with group count.
  Run it against `main` first to confirm it goes red.
- Extend `live-updates.spec.ts` to assert a hint still refreshes status /
  last-result cells in **every** group section after batching — the damper and
  the live-refresh contract must survive.
- Add a 429 test for (c): stub a 429 + `Retry-After: 2`, assert exactly one
  retry lands no earlier than ~2s (and that no retry storm precedes it).
- Existing `checks.spec.ts` and `check-groups.spec.ts` must stay green —
  per-group collapse, search, filters, group CRUD, and infinite scroll all
  route through the code being restructured.

### Open questions

- Should a group with more checks than the loaded page show a "load more"
  affordance of its own, or is page-level scroll enough? Page-level is simpler
  and matches the batched model; per-group would reintroduce per-group fetches.
- Is `limit: 100` right, or should the page size adapt to the org's total check
  count (`sum(group.checkCount)`, already known before the first checks fetch)?

## Implementation Plan

### Primary (a): one batched query, grouped client-side

1. `web/dash0/src/routes/orgs/$org/checks.index.tsx`:
   - Add a single `useInfiniteChecks(org, {with: "last_result", q, internal,
     labels, status, limit: 100})` in `ChecksIndexPage` — no `checkGroupUid`
     filter. Its query key `["checks", "infinite", org, options]` still matches
     `handleRefresh` and the change-group flow's
     `invalidateQueries(["checks", "infinite", org])`, and the live-invalidation
     `infiniteOrgRoot("checks")` predicate (which only inspects `key[0..2]`).
   - Bucket the flattened pages by `check.checkGroupUid` in a `useMemo`:
     `checksByGroup: Map<groupUid, Check[]>` plus `ungroupedChecks: Check[]`
     (ungrouped = falsy `checkGroupUid`, matching the server's
     `checkGroupUid=none`). Page order preserved within a bucket; section order
     stays driven by the groups list `sortOrder`.
   - Move the `IntersectionObserver` sentinel to the page level (one, rendered
     below the ungrouped section) so pagination is page-wide, not per-group.
   - Turn `CheckGroupSection` and `UngroupedChecksSection` into presentational
     components: drop their own `useInfiniteChecks`/sentinels; accept
     `checks: Check[]`, `isLoading`, `error` props. Keep each group's local
     `collapsed` state and the expand-on-search effect. Group count badges stay
     sourced from `group.checkCount`.
   - Filters (`q`/`internal`/`labels`/`status`) apply page-wide via the single
     query key. Collapsing groups no longer affects request cost (there is one
     query), which also removes the "collapsed group still queries" waste.

### Secondary (c): honor `Retry-After` on 429

2. `web/dash0/src/main.tsx`: special-case HTTP 429 in the QueryClient `retry`
   predicate ahead of the `status < 500` bail-out so 429s are retried
   (`failureCount < 3`); make `retryDelay(attempt, error)` return
   `min(error.retryAfter * 1000, 60_000)` when `ApiError.retryAfter` is present
   (already parsed in `client.ts`), else the existing exponential backoff. The
   60s cap prevents a hostile/absurd header from wedging a query.

### Constraints honored

- `LIVE_INVALIDATE_MIN_INTERVAL_MS` (3s damper) untouched; no revert of
  `a0b16df0`; no backend change (`limit: 100` is the server cap).

### Verification (e2e in `web/dash0/e2e/`)

3. New `checks-request-fanout.spec.ts`: seed N labeled groups+checks, deep-link
   `?labels=…` so the batched query returns a single page, count
   `GET …/checks` list requests, assert the count does **not** scale with group
   count (`< N`) — red against the pre-change N+1 code.
4. Extend `live-updates.spec.ts`: a hint refreshes a row in **every** group
   section after batching (damper + live-refresh contract survive).
5. New `checks-retry-after.spec.ts`: stub a 429 + `Retry-After: 2`, assert
   exactly one retry lands no earlier than ~2s (honors the header, not the ~1s
   backoff) with no storm before it.
6. Existing `checks.spec.ts` / `check-groups.spec.ts` stay green.
