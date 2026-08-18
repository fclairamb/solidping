---
model: sonnet
effort: high
---

# Incidents list does N+1 enrichment queries — ~900ms for a limit=50 page

## Problem

`GET /api/v1/orgs/stonal/incidents?limit=50` takes ~900ms on solidping.k8xp.com
even though it returns very little data.

The base query is **not** the problem — there are no joins at all.
`db.ListIncidents` (`server/internal/db/postgres/postgres.go:3065`) is one
filtered `SELECT ... ORDER BY started_at DESC LIMIT n` plus one `COUNT`, both
index-friendly. The cost is in the **per-row enrichment loop** in
`incidents.Service.ListIncidents` (`server/internal/handlers/incidents/service.go:1905`):

1. **`buildCheckMap`** (`service.go:1833`) — when `with=check` is requested,
   it calls `s.db.GetCheck` **once per distinct check UID, sequentially**.
   dash0 always sends `with=check`
   (`web/dash0/src/routes/orgs/$org/incidents.index.tsx:111`), so a 50-row
   page can cost up to 50 extra round trips.
2. **`loadIncidentMembers`** (`service.go:1797`) — runs **unconditionally**
   for every group incident (not even gated behind a `with`): one
   `ListIncidentMemberChecks` + one `GetCheck` **per member** + one
   `GetCheckGroup`. A group incident with 10 members costs 12 queries on its
   own.

A page of 50 incidents with a few group incidents easily fires 100–200
sequential point lookups. At 2–5ms per round trip (pod → Postgres), that is
exactly the observed ~900ms. The freshly-landed DB metrics with callsite
labels (batch/2026-08-17) should confirm this in production as a storm of
fast-individually, slow-in-aggregate `GetCheck` calls.

The checks list endpoint already solves the same problem correctly:
`GetLabelsForChecks` and `GetLastResultForChecks` are single batched
`WHERE ... IN (...)` queries for the whole page
(`server/internal/handlers/checks/service.go:927` and `:951`). Incidents is
the laggard.

## Proposal

Two changes, both decided (API changes are fine — pre-1.0):

**A. Gate member enrichment behind `with=members`.** `loadIncidentMembers`
currently runs unconditionally for every group incident. Make it opt-in via
`with=members` on **both** `ListIncidents` and `GetIncident` (the `with`
parser already exists in both — `applyListIncidentsExtras` at
`server/internal/handlers/incidents/handler.go:112` and the `GetIncident`
options at `handler.go:136`). `checkGroupSlug` is populated by the same
loader and moves with it. This is safe: as of today **nothing consumes
either field** — dash0 never reads `members`/`checkGroupSlug` from incident
responses (the detail page's `useMembers` is org *user* members), and
neither do the MCP incident tools or the notification surfaces. With dash0
not sending `with=members`, the default list does **zero** member queries.
Update `wiki/api-specification/` accordingly.

**B. Batch what remains.**

1. **Add batch lookups to the DB layer** (Postgres + SQLite + the
   `db.Service` interface at `server/internal/db/service.go` + every mock
   that implements it):
   - `GetChecksByUIDs(ctx, orgUID, checkUIDs []string) (map[string]*models.Check, error)`
   - `ListIncidentMemberChecksByIncidentUIDs(ctx, incidentUIDs []string) (map[string][]*models.IncidentMemberCheck, error)`
   - `GetCheckGroupsByUIDs(ctx, orgUID, groupUIDs []string) (map[string]*models.CheckGroup, error)`
2. **Rework `ListIncidents` enrichment** to collect the page's check UIDs
   (for `with=check`) and, when `with=members`, the group-incident UIDs /
   member check UIDs / group UIDs, then issue **at most 4 batch queries
   total** (checks, member rows, member checks, groups) regardless of page
   size. `buildCheckMap` and `loadIncidentMembers` collapse into this.
3. **Tests**:
   - Response shape for each field is unchanged; only presence is now gated:
     tests must prove `members`/`checkGroupSlug` appear with `with=members`
     and are absent without it (list and single-get), and that
     `checkSlug`/`checkName`/`check` still behave under `with=check`.
   - Add a query-count test (bun `QueryHook` counter) proving a
     multi-incident page with group incidents and
     `with=check,members` costs a constant number of queries — and that
     without `with=members` no member/group queries run at all — with a
     positive control showing the counter does count (per feedback: prove
     the negative).

### Non-goals / assessed alternatives (for the record)

- **Splitting the API into core + `?with=subPart` lazy parts**: already
  exists (`with=check` on incidents, `with=last_result,last_status_change`
  on checks) and is not the fix — dash0 needs those parts on first paint, so
  deferring them just moves the 900ms to a second request. The parts are slow
  because of N+1, not because they are requested.
- **Delta-fetch for the checks page** ("only fetch checks changed since last
  query"): rejected for now. dash0 already has a WebSocket live-hints layer
  with damped invalidation plus a deliberate 10s poll
  (`web/dash0/src/contexts/LiveEventsContext.tsx:60`, spec 2026-08-09-07),
  and the checks-list query was already reduced to one index descent per
  check (~47ms). Delta sync would add deletion tombstones, watermark/clock
  races, and client-side merge state to save a payload gzip already shrinks.
  If payload cost ever becomes real, an `ETag`/`If-None-Match` → 304 on the
  unchanged poll is a far simpler follow-up spec.
- **The unconditional `COUNT` per request** (`postgres.go:3090`): a second
  full-filter query on every page load. Keep it for now (it feeds
  `pagination.total`), but it is the next candidate if the endpoint is still
  slow after batching.

## Implementation Plan

### Pre-verification (done before coding)
Re-checked the spec's claim that nothing consumes `members`/`checkGroupSlug` today:
- dash0: no reference to `.members` or `checkGroupSlug` on incident responses
  anywhere in `web/dash0/src` (the only `members` hits are the unrelated org
  *user* members page/route).
- MCP incident tools (`server/internal/mcp/tools_incidents.go`): only ever set
  `opts.WithCheck`, never reference `.Members` or `.CheckGroupSlug` on the
  response.
- Notification/jobs code (`server/internal/notifications`, `server/internal/jobs`):
  no references to `.Members` or `.CheckGroupSlug`.
Confirmed safe to gate both fields behind `with=members`.

### A. Gate member enrichment behind `with=members`
1. `ListIncidentsOptions` / `GetIncidentOptions`: add `WithMembers bool`.
2. `handler.go`: `applyListIncidentsExtras` and the `GetIncident` with-parser
   both recognize `members` alongside `check`.
3. `service.go`: `loadIncidentMembers` (renamed/reworked into the batched
   path below) only runs when `opts.WithMembers` is true, for both
   `ListIncidents` and `GetIncident`.
4. Update `wiki/api-specification/results-incidents.md` — both `with` lines
   (list and single-get) become `` `with` - comma-separated: `check`, `members` ``.

### B. Batch DB layer
1. Add three methods to `db.Service` interface (`server/internal/db/service.go`),
   Postgres (`server/internal/db/postgres/postgres.go`), SQLite
   (`server/internal/db/sqlite/sqlite.go`), and the one hand-rolled mock that
   implements the full interface (`server/internal/notifications/slack_test.go`,
   guarded by `var _ db.Service = (*mockDBService)(nil)`):
   - `GetChecksByUIDs(ctx, orgUID string, checkUIDs []string) (map[string]*models.Check, error)`
   - `ListIncidentMemberChecksByIncidentUIDs(ctx, incidentUIDs []string) (map[string][]*models.IncidentMemberCheck, error)`
   - `GetCheckGroupsByUIDs(ctx, orgUID string, groupUIDs []string) (map[string]*models.CheckGroup, error)`
   Mirror the empty-input short-circuit and `WHERE ... IN (?)` / `bun.List(...)`
   shape of `GetLabelsForChecks` (`postgres.go:1974`, `sqlite.go:1921`). No
   chunking needed — incident page size is capped at 100 (`base.ParsePageLimit`),
   far under SQLite's ~999 variable limit.
2. Rework `ListIncidents` enrichment (`service.go:1905`) to:
   - collect distinct check UIDs from the page (existing `with=check` logic),
     call `GetChecksByUIDs` once.
   - when `opts.WithMembers`: collect incident UIDs of group incidents
     (`inc.CheckGroupUID != nil`), call `ListIncidentMemberChecksByIncidentUIDs`
     once; collect the distinct member check UIDs across all returned rows and
     call `GetChecksByUIDs` (reuse/merge with the check-UID batch since both
     want `map[string]*models.Check`); collect distinct group UIDs and call
     `GetCheckGroupsByUIDs` once.
   - total DB calls: at most 4 (checks, member rows, member-check checks,
     groups) — checks batch is shared between `with=check` and member-check
     resolution so it's a single call, not two.
   - `buildCheckMap` and `loadIncidentMembers` collapse into this new
     `enrichIncidents` path; `GetIncident` reuses the same per-incident
     assembly helpers (member/group building) but naturally does only 1 lookup
     each since it's a single incident.
3. Response shape per field is unchanged — only gating changes.

### C. Tests
1. `handler_test.go` / `service_test.go`: prove `with=members` presence/absence
   for both list and get; prove `with=check` behavior unchanged.
2. New query-count test using a bun `QueryHook` counter against the SQLite
   in-memory DB used by `service_test.go`: multi-incident page with group
   incidents, `with=check,members` — assert a constant, small query count
   independent of incident/member count (add more fixture rows, assert count
   doesn't grow). Assert zero member/group queries when `with=members` is
   absent. Positive control: assert the counter is non-zero / fires on a known
   query, so an inert hook can't fake a passing bound.
