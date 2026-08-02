---
model: sonnet
effort: high
---

# The dashboard "Active incidents" KPI caps at 5 because the incidents list endpoint returns no total

## Problem

The org dashboard's **Active incidents** KPI tile under-reports whenever an org
has more than 5 active incidents. It always shows at most `5`.

In [dashboard-page.tsx:295](web/dash0/src/components/dashboard/dashboard-page.tsx:295)
and [:321](web/dash0/src/components/dashboard/dashboard-page.tsx:321):

```ts
const incidents = incidentsQuery.data?.data || [];
...
const incidentsCount = incidents.length;
```

`incidentsQuery` is `useIncidents(org, { state: "active", size: 5, ... })`
([dashboard-page.tsx:252](web/dash0/src/components/dashboard/dashboard-page.tsx:252)),
so `incidents.length` is bounded by the requested page size. The tile renders
`incidentsCount` at
[:456](web/dash0/src/components/dashboard/dashboard-page.tsx:456) and it also
feeds the banner copy at
[:642](web/dash0/src/components/dashboard/dashboard-page.tsx:642)
(`t("banner...", { incidents: incidentsCount })`) — both display a truncated
number.

This is the same class of bug that
[specs/done/…/2026-08-02-06-dashboard-check-stats-api.md](specs/todos/2026-08-02-06-dashboard-check-stats-api.md)
fixed for the **check** counters; the incident counter was missed.

**It cannot be fixed client-side.** Unlike the checks list, the incidents list
endpoint returns no total. See
[server/internal/handlers/incidents/service.go:1700](server/internal/handlers/incidents/service.go:1700):

```go
// PaginationResponse represents pagination info.
type PaginationResponse struct {
	Cursor string `json:"cursor,omitempty"`
	Size   int    `json:"size"`
}
```

Contrast the checks handler, whose `PaginationResponse` carries
`Total int64 \`json:"total"\`` ([checks/service.go:795](server/internal/handlers/checks/service.go:795)),
populated from `s.db.ListChecks(ctx, org.UID, filter)` which returns
`(checks, total, err)` ([checks/service.go:847](server/internal/handlers/checks/service.go:847),
[:962](server/internal/handlers/checks/service.go:962)).

The frontend is already wired for it — `useIncidents` maps
`response.pagination?.total` into its return value
([hooks.ts:1100](web/dash0/src/api/hooks.ts:1100)), and the local
`CursorPagination` interface already declares `total?: number`
([hooks.ts:258](web/dash0/src/api/hooks.ts:258)). Today that field is simply
always `undefined` on the wire.

Note: `allGreen` (`downCount === 0 && incidentsCount === 0`, at
[dashboard-page.tsx:400](web/dash0/src/components/dashboard/dashboard-page.tsx:400))
stays **correct** either way — a truncated count is only ever wrong in the
`> 0` direction. Only the *displayed number* is wrong. So this is a display
accuracy fix, not a state-machine fix.

## Proposal

### 1. Backend — return a total from the incidents list

Add `Total int64 \`json:"total"\`` to
`incidents.PaginationResponse` ([service.go:1700](server/internal/handlers/incidents/service.go:1700))
and populate it in `Service.ListIncidents`
([service.go:1876](server/internal/handlers/incidents/service.go:1876)).

There is currently **no** `CountIncidents` anywhere in the DB layer (verified:
`grep -rn "CountIncidents" server/` returns nothing). Two options:

- **(a) Mirror the checks precedent** — change the DB-layer
  `ListIncidents(ctx, filter)` to return `(incidents, total, err)`, like
  `ListChecks` already does. Fewer moving parts, one round trip through the same
  filter object, and the count can't drift from the list.
- **(b) Add a separate `CountIncidents(ctx, filter)`** to the `db.Service`
  interface.

**Prefer (a)** — it matches the existing checks pattern exactly and structurally
guarantees the COUNT uses the same filter as the SELECT. Either way the change
must land in **both** backends:
- [server/internal/db/postgres/postgres.go](server/internal/db/postgres/postgres.go)
- [server/internal/db/sqlite/sqlite.go](server/internal/db/sqlite/sqlite.go)
- interface in [server/internal/db/service.go](server/internal/db/service.go)

**The COUNT must apply exactly the same predicates as the list**, minus
limit/cursor. That is the main correctness risk here — the incidents filter is
not trivial:
- `state` including the derived `acked` / `snoozed` values, which the handler
  translates into `active` + an extra filter
  (`parseListIncidentsOptions`, [handler.go:63](server/internal/handlers/incidents/handler.go:63))
- `hideSuppressed`
- `checkUid` after slug→UID resolution
- `since` / `until`
- `causedByIncidentUid`

Build the count from the **same** `*models.ListIncidentsFilter` produced by
`buildListIncidentsFilter` ([service.go:1840](server/internal/handlers/incidents/service.go:1840));
do not hand-roll a second WHERE clause.

Two edge cases to preserve:
- The early-return short-circuit at
  [service.go:1898](server/internal/handlers/incidents/service.go:1898) (all
  requested `checkUid`s failed to resolve → explicit empty page) must return
  `Total: 0`, not an unset/garbage value.
- `hasMore` / cursor behaviour must be unchanged.

### 2. Frontend — read the total for the KPI

In [dashboard-page.tsx](web/dash0/src/components/dashboard/dashboard-page.tsx),
change `incidentsCount` to prefer the server total and fall back to the page
length:

```ts
const incidentsCount = incidentsQuery.data?.total ?? incidents.length;
```

Keep rendering the 5-row list from `incidents` — only the tile number and the
banner copy switch to the total. No change is needed in `useIncidents`
([hooks.ts:1055](web/dash0/src/api/hooks.ts:1055)); it already forwards
`pagination.total`.

Check whether other call sites of `useIncidents` derive a count from
`data.data.length` and would benefit from the same treatment; fix them or note
them explicitly.

### 3. Docs

- [server/internal/app/openapi/openapi.yaml](server/internal/app/openapi/openapi.yaml) —
  add `total` to the incidents list pagination schema.
- [wiki/api-specification/results-incidents.md](wiki/api-specification/results-incidents.md) —
  document the new `total` field on `GET /api/v1/orgs/:org/incidents` (and any
  sibling incident-list routes that share the response shape, around lines
  37–51 and 110–111).

### 4. Tests

- **Backend, the proving test**: seed **more active incidents than the page
  size** (e.g. 7 active with `limit=5`), assert `len(data) == 5` **and**
  `pagination.total == 7`. This is the test that actually proves the bug is
  fixed — a test with fewer incidents than the page size passes both before and
  after.
- **Positive control on filtering**: seed a mix (active + resolved, and
  suppressed vs not) and assert `total` reflects the *filtered* set, not the
  org-wide incident count. A count that ignores the filter would otherwise
  silently pass the first test.
- Cover both backends (SQLite + Postgres/testcontainer), consistent with how
  the incidents package already splits `list_by_slug_test.go` and
  `list_by_slug_postgres_test.go`.
- **Frontend**: a dash0 E2E or component-level assertion that the KPI tile shows
  the total rather than the page size when the API reports `total > size`.

## Implementation Plan

1. **DB interface** (`server/internal/db/service.go`): change
   `ListIncidents(ctx, filter) ([]*models.Incident, error)` to
   `([]*models.Incident, int64, error)`, mirroring `ListChecks`.
2. **Postgres + SQLite** (`server/internal/db/postgres/postgres.go`,
   `server/internal/db/sqlite/sqlite.go`): extract a shared
   `applyIncidentsFilter(query, filter)` helper that applies every predicate
   except cursor/limit. Build the list query and a `countQuery` from the same
   helper so they can't drift; keep cursor + `Limit` only on the list query;
   run `countQuery.Count(ctx)` and return it as the second value.
3. **Update the 5 call sites** of `db.ListIncidents` for the new signature:
   `handlers/incidents/service.go` (uses the total), `handlers/checks/service.go`,
   `handlers/escalationpolicies/service.go`, `handlers/availability/service.go`,
   `integrations/msteams/mention_commands.go`,
   `integrations/slack/mention_commands.go` (the last 5 discard the count with `_`).
4. **incidents/service.go**: add `Total int64 \`json:"total"\`` to
   `PaginationResponse`; populate it from the new `total` return value in
   `ListIncidents`; keep the early-return short-circuit explicit at `Total: 0`.
5. **Frontend** (`web/dash0/src/components/dashboard/dashboard-page.tsx`):
   change `incidentsCount` to `incidentsQuery.data?.pagination?.total ?? incidents.length`
   (verify actual field name/shape returned by `useIncidents` before wiring —
   the spec's snippet assumed `data.total` but the wire shape is
   `data.pagination.total` per `hooks.ts`). Audit other `useIncidents` call
   sites for the same truncation bug.
6. **Docs**: add `total` to the incidents list pagination schema in
   `server/internal/app/openapi/openapi.yaml` and to
   `wiki/api-specification/results-incidents.md`.
7. **Tests**:
   - Backend: extend `list_by_slug_test.go` (SQLite) and
     `list_by_slug_postgres_test.go` (Postgres) with the proving test (7 active,
     limit 5 → `len==5`, `total==7`) and the filtered-total positive control.
   - Frontend: extend/add a dash0 test asserting the KPI tile shows `total`
     over page length when `total > size`.
