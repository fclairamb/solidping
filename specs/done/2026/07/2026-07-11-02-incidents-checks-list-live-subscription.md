# Incidents and checks list pages never subscribe to live updates

## Problem

On `https://solidping.k8xp.com/dash0/orgs/acmetech/incidents?state=all` the
incidents list does not receive live updates: no `subscribe` frame is sent on
the realtime WebSocket, so the page only refreshes on the lazy poll interval
(or a manual reload). The same is true of the checks list at
`/dash0/orgs/acmetech/checks`.

The realtime plumbing (spec `2026-07-02-02-realtime-websocket-per-entity-subscriptions`)
is fully in place and already used elsewhere:

- The org dashboard subscribes to the org-wide collections —
  `useLiveSubscription({ entity: "checks" | "incidents" | "events" })`
  (`web/dash0/src/components/dashboard/dashboard-page.tsx:215-217`).
- The check detail page subscribes to its per-check scope plus incidents
  (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:435-436`).
- Hint events are mapped to TanStack Query invalidations via
  `DEFAULT_QUERY_ROOTS` (`web/dash0/src/contexts/LiveEventsContext.tsx:97`):
  entity `incidents` invalidates the `incidents`/`incident` org roots, and
  entity `checks` invalidates the `checks` org root — including on kind
  `results`, so the embedded `lastResult` / "last checked" column stays fresh.

But the two list routes never call `useLiveSubscription` at all:

- `web/dash0/src/routes/orgs/$org/incidents.index.tsx` fetches with
  `useIncidents(org, …)` (line 117) and has no live subscription.
- `web/dash0/src/routes/orgs/$org/checks.index.tsx` likewise has no live
  subscription.

So the pages whose whole purpose is watching incidents/checks are the ones
that don't update live, while the dashboard summary does.

## Proposal

Opt both list pages into the existing org-wide entity subscriptions:

1. **Incidents list** (`incidents.index.tsx`): add
   `useLiveSubscription({ entity: "incidents" })` in the page component. The
   `DEFAULT_QUERY_ROOTS.incidents` mapping already invalidates the
   `incidents` query root that `useIncidents` uses (it's the same hook the
   dashboard relies on), so no new invalidation wiring should be needed.
   Verify this holds for all `state` filter variants (`?state=all`, `open`,
   etc.) — the invalidation matches the query root, so filtered variants
   should refetch too.
2. **Checks list** (`checks.index.tsx`): add
   `useLiveSubscription({ entity: "checks" })`. Kind `results` already maps
   to the `checks` org root, so steady-state runs (no status transition)
   refresh the "last checked" / last-result cells as well.
3. **Tests**: extend the live-updates E2E coverage (see
   `specs/done/2026/07/2026-07-10-03-live-updates-silent-failure-e2e.md`) or
   add unit coverage asserting each list page registers its scope, so a
   future refactor can't silently drop the subscription again — that is
   exactly how this regression went unnoticed.

Open questions:

- Should other list pages be audited for the same omission (e.g. events /
  recent-activity standalone views), or is the dashboard the only other
  consumer by design?
- The incidents page renders check names alongside incidents; if those come
  from the same `useIncidents` payload nothing more is needed, but if a
  separate checks query feeds the page, consider subscribing to
  `{ entity: "checks" }` there too.

## Implementation Plan

### Findings that shape the plan

- **The checks list page's queries are invisible to today's invalidation
  mapping.** `checks.index.tsx` fetches exclusively through
  `useInfiniteChecks`, whose query key is `["checks", "infinite", org,
  options]` — but `DEFAULT_QUERY_ROOTS.checks` only carries
  `orgRoot("checks")`, whose predicate requires `key[1] === org`. For the
  infinite key, `key[1] === "infinite"`, so no hint would ever invalidate the
  list even after subscribing. Adding `useLiveSubscription({ entity:
  "checks" })` alone would send the subscribe frame and then invalidate
  nothing the page uses. The mapping needs a new root shape first.
  (The existing unit test file even seeds `["checks", "infinite", ORG, {}]`
  but never asserts it goes stale — the gap was seeded, never covered.)
- **The incidents list is already covered by the mapping.**
  `useIncidents` keys are `["incidents", org, queryOptions]` and
  `orgRoot("incidents")` matches on `key[0]`/`key[1]` only, so every `state`
  filter variant (`all`, `active`, `acked`, `snoozed`, `resolved`) and the
  `showSuppressed` variant refetch on invalidation. Only the subscription
  call is missing.
- **Open question 2 answered: no extra checks subscription needed on the
  incidents page.** The check name/slug rendered per row comes from the same
  `useIncidents` payload (`with: "check"` embeds `checkName`/`checkSlug`);
  no separate checks query feeds that page.
- **Open question 1 (audit other pages):** the dashboard subscribes to
  `checks`/`incidents`/`events`; the jobs hooks subscribe to `jobs`; the
  check detail page subscribes to its per-check scope + `incidents`. There is
  no standalone events/recent-activity list route — the recent-activity feed
  only exists inside the dashboard, which already subscribes. The two list
  pages in this spec are the only omissions.
- **Neither list page polls today** (no `refetchInterval` passed), so there
  is no base poll to stretch via `stretchWhileLive`/`useScopeLive` — the
  subscription is purely additive.

### Steps

1. **Mapping fix** (`web/dash0/src/contexts/LiveEventsContext.tsx`): add an
   `infiniteOrgRoot` helper matching `key[0] === root && key[1] ===
   "infinite" && key[2] === org`, and register `infiniteOrgRoot("checks")`
   under both `checks.checks` and `checks.results` kinds so the paginated
   checks list refreshes on status transitions and steady-state results
   alike.
2. **Incidents list subscription**
   (`web/dash0/src/routes/orgs/$org/incidents.index.tsx`): add
   `useLiveSubscription({ entity: "incidents" })` in `IncidentsIndexPage`.
3. **Checks list subscription**
   (`web/dash0/src/routes/orgs/$org/checks.index.tsx`): add
   `useLiveSubscription({ entity: "checks" })` in `ChecksIndexPage`.
4. **Unit tests** (`web/dash0/src/contexts/LiveEventsContext.test.ts`):
   assert the seeded `["checks", "infinite", ORG, {}]` key goes stale on the
   subscribed ack and on `results`/`checks`-kind hints for the `checks`
   scope, and that another org's infinite key is never touched.
5. **E2E** (`web/dash0/e2e/live-updates.spec.ts`): add a
   `waitForScopeSubscribed(page, entity)` helper that asserts the
   `subscribed` ack for the *specific* entity (locks in "this page registers
   its scope" so a refactor can't silently drop it), then two tests driven by
   heartbeat pushes (the deterministic live trigger):
   - incidents list at `?state=active` (exercises a filtered variant):
     heartbeat `down` opens an incident that must appear in the table
     without reload well under the no-poll fallback; heartbeat `up` resolves
     it and the row must disappear live.
   - checks list: heartbeat `down` must flip the check row's status badge to
     down without reload.
6. **QA**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new
   errors in touched files), `bun run test:unit`; run the two new E2E tests
   against a test-mode server if reachable, otherwise report
   authored-but-not-run.
