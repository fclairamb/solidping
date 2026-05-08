# Check dependencies and cascading-incident rollup

## Context

Today every failing check opens its own incident and pages independently. When
a shared dependency fails — RabbitMQ, the primary database, an upstream API —
every check that consumes it fails moments later, and SolidPing pages on each
of them as if they were separate problems. The operator gets N pages for what
is really one incident, the on-call rota burns out, and the *root cause* is
buried in a list of derived noise.

What's missing is a notion of **service dependency**: that check `worker-a`
needs `rabbit`, and `web-frontend` needs `worker-a`, so a `rabbit` outage
causes everything downstream. Once that graph exists, SolidPing can:

1. Identify a derived failure as derived (not a fresh incident to page about),
2. Show the *blast radius* of an active incident at a glance,
3. Re-evaluate downstream incidents when the root cause resolves.

This spec adds the data model, the correlation algorithm, the API, and the
minimum frontend surface needed to make this useful. It deliberately does *not*
ship a graph diagram view — see "Out of scope" for why.

It builds on the paging substrate already in place:
- Incident ack/snooze (`2026-05-02-16-incident-ack-snooze.md`).
- On-call schedules (`2026-05-02-17-on-call-schedules.md`).
- Escalation policies (`2026-05-02-19-escalation-policies.md`).

The escalation worker becomes the load-bearing integration point: it must
honor a new `paging_suppressed` flag at fire time, otherwise a child incident
attached to a parent *after* its first step was queued will still page.

## Scope

In scope:
- New `check_dependencies` table: directed edge
  `(parent_check_uid → child_check_uid)` with `kind ∈ {hard, soft}` and an
  optional human-readable description.
- DAG enforcement: cycle detection at write time, self-edges rejected,
  cross-org edges rejected.
- Two new columns on `incidents`: `caused_by_incident_uid` (FK to incidents,
  nullable) and `paging_suppressed` (bool, default false).
- Incident-open hook: walk parents, pick a root cause, set the new columns.
- Incident-resolve hook: re-evaluate children, fire paging now if any are
  still down for non-attributed reasons.
- API: per-check dependency CRUD plus an org-wide read-only graph endpoint.
- Frontend (dash0): dependency picker on check edit; "Depends on" /
  "Depended on by" cards on the check detail page; "Blast radius" panel on
  the incident detail page; an org-level dependency list page.

Out of scope (own future specs):
- **Visual graph/diagram view.** Tempting, but graphs of >30 nodes are
  unreadable, react-flow integration is heavy, and the operational need is
  satisfied by the table view + per-check cards + blast-radius panel. Add a
  diagram view in a follow-up only if users actually ask. The data model
  here is sufficient — the org-graph endpoint already returns nodes + edges.
- **Severity-weighted SLA propagation.** Interesting research direction, not
  needed for the alerting payoff.
- **Status-page integration** ("show parent-caused outages as a single
  banner with affected services as a sub-list"). The data model leaves room
  for this — see "Risks" for a note.
- **Cross-org dependencies.** No clean security or scoping story; not worth
  it for v1.

## Why hard/soft and not a criticality slider

A 1–5 slider (or low/medium/high) sounds intuitive but is the wrong abstraction.
The slider has no operational meaning — it just punts the suppression
threshold to a number, and users default to "medium" because it feels safe.
The actually load-bearing distinction is binary:

- **`hard`**: the child *will* fail when the parent fails. The web frontend
  needs the database; a worker needs the queue it consumes from; a service
  needs its reverse-proxy upstream. When a hard parent is down within the
  correlation window, the child's incident is rolled up under it — paging
  fires only on the parent.
- **`soft`**: the child *may* degrade but won't necessarily fail. Analytics,
  log shipping, an optional cache, a metrics push endpoint. A soft parent
  failure is *informational* on the child's incident detail page but does
  **not** suppress paging — the child's failure may well be unrelated.

Two values, both meaning something concrete. If we ever discover a third
distinct mode (we won't), it can be added; sliders are easy to add later and
hard to walk back.

## Why "rollup" and not "suppression"

A naïve read of "don't report cascading failures, only the initial one" leads
to *suppressing* child incidents entirely — never opening them while a parent
is down. Don't do that. Three reasons:

1. The on-call needs to answer "what's affected?" the moment they're paged on
   the parent. If children are hidden, that information is gone.
2. A child can fail for *unrelated* reasons during a parent outage. Pure
   suppression masks real outages and surfaces them only after the parent
   recovers — by which point the operator has moved on.
3. A child that stays down *after* the parent recovers is the most important
   diagnostic signal in the entire feature. It says "fixing the root cause
   didn't fix this — there's a second problem."

So child incidents still open. They appear in the incident list. They produce
timeline events. They're tagged with `caused_by_incident_uid = <parent>` and
`paging_suppressed = true`. The parent incident gains a "Blast radius" panel
listing them. When the parent resolves, children are re-evaluated:
**still down → paging fires now**, recovered → no-op. The on-call gets the
visibility benefit and only one page per actual problem.

## Data model

### `check_dependencies` (new table)

| Column | Type | Notes |
|---|---|---|
| `uid` | varchar(36) PK | |
| `organization_uid` | FK | Same as both checks; enforced at write time. |
| `parent_check_uid` | FK | The check that, when down, causes the child. |
| `child_check_uid` | FK | The dependent check. |
| `kind` | enum | `hard` \| `soft`. |
| `description` | text nullable | Free-form, surfaced in the UI. |
| `created_at` / `updated_at` / `deleted_at` | | |

Constraints:
- Unique on `(parent_check_uid, child_check_uid)` — one edge per ordered pair.
- `parent_check_uid != child_check_uid` (CHECK constraint, app-level guard
  too).
- Index on `child_check_uid` (used for the parent walk on incident open).
- Index on `parent_check_uid` (used for the child walk on parent resolve).

Both checks must belong to the same org (validated in the service; no FK
constraint can express it directly without a redundant column).

### `incidents` (additions)

```go
CausedByIncidentUID *string `bun:"caused_by_incident_uid"`         // FK to incidents.uid
PagingSuppressed    bool    `bun:"paging_suppressed,notnull,default:false"`
```

`caused_by_incident_uid` references the *root cause incident* (closest hard
ancestor at the time the child opened). `paging_suppressed` is the single
source of truth for "skip notifications and escalation." It can flip from
`true` to `false` when the parent resolves and the child is still down — see
the resolve hook.

### Migration

One migration file. Match the pattern of recent migrations (search the dir
for the highest existing number). Adds the table + two columns. All new
columns nullable except `paging_suppressed`, which has a default.

## Correlation algorithm

The hook lives in `server/internal/handlers/incidents/service.go`, at the
spot where new incidents are created (immediately before the existing
`queueNotifications` call).

### On incident open

```
1. Let now = incident.started_at, child = the check.
2. Walk parents BFS, depth cap = 10 (DEPTH_CAP).
   - From child, follow check_dependencies rows where child_check_uid = current.
   - Track the path's edge kinds: a path is "hard" if every edge is hard.
   - Skip soft-edge paths for rollup purposes (they're informational only).
3. For each ancestor reached via at least one fully-hard path, query open
   incidents on that check whose started_at is within
       [now - max(2 * child.period, 5min), now]   (CORRELATION_WINDOW)
   and whose paging_suppressed = false (don't roll up under another
   already-rolled-up incident).
4. Candidates =
     {(ancestor_incident, depth = length of shortest hard path from child)}.
5. If candidates is empty: open as normal (no rollup).
6. Else: pick the deepest one (max depth) — that's the "most root" cause
   reachable. Tie-break: oldest started_at.
7. Set:
       new_incident.caused_by_incident_uid = chosen.uid
       new_incident.paging_suppressed       = true
   Emit incident.rollup_attached event with payload
       {root_incident_uid, depth}.
   Skip queueNotifications. Skip escalation policy scheduling.
```

`DEPTH_CAP = 10` and `CORRELATION_WINDOW = max(2 * child.period, 5min)` are
constants in code. Adjustable in a future patch but not org-configurable in
v1 — start simple.

### On incident resolve (parent)

```
1. After the existing resolve flow updates the incident:
2. Find children with caused_by_incident_uid = self.uid AND state = open
   AND paging_suppressed = true.
3. For each child:
   a. Re-fetch the child's check status.
   b. If status != down → no-op (the child has recovered). Optionally emit
      incident.rollup_detached event to record that the rollup ended.
   c. If status == down → this is the load-bearing case:
      - Set paging_suppressed = false.
      - Emit incident.rollup_detached event (so the timeline shows it).
      - Call queueNotifications for this incident now.
      - If the child's check (or check_group) has an escalation policy,
        call the same scheduling entrypoint spec 19 uses — with the *child's*
        incident as the target. Steps schedule from now, not from the
        original started_at, otherwise we'd fire all of them at once.
4. Don't recurse: a freshly-detached child doesn't auto-trigger paging on its
   *own* descendants. They were already attached to a downstream rollup root
   (or none); their paging state is independent.
```

### What soft edges do

Soft edges never cause rollup. They appear in two informational places:
- The incident detail page lists "Soft-related incidents" if any soft parent
  has an open incident in the correlation window — purely a UI hint.
- The dependency picker UI shows them with a different badge color so the
  operator can tell at a glance which dependencies will suppress paging.

### Escalation worker integration

Spec 19's escalation step worker runs when a job fires and looks up the
incident. **It must check `paging_suppressed` at fire time** (not just at
schedule time). If true → exit cleanly, emit
`incident.escalation_skipped_suppressed` event. This is a one-line addition
inside the existing early-exit block (alongside the ack / snooze /
resolve check).

This matters because: incident A opens at T0, no parent yet → escalation
step scheduled for T0+10min. At T0+30s, parent goes down and we need to
attach A to it (this only happens if there's a parent retroactively reachable
within the window — see the symmetry note below). Or, more commonly: A is
opened *after* the parent, the rollup is set immediately, but the
escalation policy was already scheduled in the same transaction before
the rollup hook ran. Either way, the fire-time check is the authoritative
gate.

Implementation: schedule the escalation steps *after* the rollup hook
runs, so the common case is handled correctly. Treat the fire-time check
as defense in depth.

## API

All routes scoped to `/api/v1/orgs/$org`.

### `GET /checks/$check/dependencies`

Returns:

```json
{
  "data": {
    "dependsOn": [
      {
        "uid": "<dep-uid>",
        "parentCheck": { "uid": "...", "slug": "rabbit", "name": "RabbitMQ" },
        "kind": "hard",
        "description": "consumes the orders queue"
      }
    ],
    "dependedOnBy": [
      {
        "uid": "<dep-uid>",
        "childCheck": { "uid": "...", "slug": "web", "name": "Web frontend" },
        "kind": "hard",
        "description": null
      }
    ]
  }
}
```

### `POST /checks/$check/dependencies`

Body:

```json
{
  "parentCheckUid": "<uid>",
  "kind": "hard",
  "description": "consumes the orders queue"
}
```

Validates:
- `parentCheckUid` exists and belongs to the same org → else
  `DEPENDENCY_CROSS_ORG` (also covers "check not found" — return the same
  code; we don't reveal cross-org existence).
- Not self → `DEPENDENCY_SELF`.
- Adding this edge does not create a cycle → `DEPENDENCY_CYCLE`. Algorithm:
  DFS from the proposed parent over the existing graph; if the child is
  reachable, reject.
- Edge does not already exist → `DEPENDENCY_DUPLICATE` (or the existing
  `CONFLICT` code; reuse what's idiomatic).

Returns the created row.

### `PATCH /checks/$check/dependencies/$uid`

Body: `kind` and/or `description`. Changing `kind` does not retroactively
re-attribute existing incidents; the column on incidents is a snapshot at the
time the rollup decision was made.

### `DELETE /checks/$check/dependencies/$uid`

Soft-delete. Open incidents with `caused_by_incident_uid` referring to a
parent through this edge are not changed — the column on incidents is the
historical record.

### `GET /dependencies`

Returns the org-wide graph as an unlabeled adjacency list, suitable for the
list page and any future graph view:

```json
{
  "data": {
    "nodes": [
      { "uid": "...", "slug": "rabbit", "name": "RabbitMQ", "status": "up" }
    ],
    "edges": [
      { "uid": "...", "parentCheckUid": "...", "childCheckUid": "...", "kind": "hard" }
    ]
  }
}
```

Pagination is unnecessary in v1 — the practical org graph is <500 edges. If
that changes, add it without breaking the shape.

### Errors

Add to `base.HandlerBase`:
- `DEPENDENCY_CYCLE`
- `DEPENDENCY_SELF`
- `DEPENDENCY_CROSS_ORG`
- `DEPENDENCY_NOT_FOUND`
- `DEPENDENCY_DUPLICATE` (or reuse `CONFLICT`)

## Frontend (dash0)

### Check edit form

Add a "Dependencies" section below the existing fields.

- A multi-row picker. Each row: `<check selector>` `<kind dropdown>`
  `<description input>` `<delete>`.
- "Add dependency" button appends a row.
- The check selector excludes self and any check that would create a cycle
  (the server will reject; the client also pre-filters using the org graph
  fetched from `GET /dependencies` to avoid frustrating UX).
- On save, diff the rows against the previously loaded ones and issue
  POST / PATCH / DELETE accordingly.

### Check detail page

Two new cards next to the existing ones:

- **Depends on** — list of parents, each with a status pill (current check
  status), kind badge (`hard`/`soft`), and the description.
- **Depended on by** — symmetric list of children. Useful for "if I touch
  this, what breaks?"

Both cards are hidden when empty.

### Incident detail page

Add a **Blast radius** card, only when the incident is the *root* of a
rollup tree (i.e., other open incidents have `caused_by_incident_uid =
self.uid`).

```
Blast radius (3 affected)
  ✗ worker-a       (down)        paging suppressed
  ✗ worker-b       (down)        paging suppressed
  ✓ web-frontend   (recovering)  paging suppressed
```

Each row links to the child incident detail page. Show a small caveat
footer: "Paging will fire on the children if this incident resolves while
they are still down."

For *child* incidents (those with `caused_by_incident_uid != null`), show
a banner at the top of the page: "Rolled up under <parent-check-name>
incident — paging is suppressed while the root cause is open."

Add a "Soft-related incidents" sidebar (collapsed by default) listing any
soft-parent incidents inside the correlation window — purely informational.

### Dependency list page

`web/dash0/src/routes/orgs/$org/dependencies.index.tsx` (new).

Plain table:

| Parent | → | Child | Kind | Description |
|---|---|---|---|---|

Filterable by check name. Sortable by parent or child. Click a row → check
detail page (parent's "Depended on by" anchor).

### i18n

New `dependencies` namespace. Mirror the existing pattern.

## Test scenarios

`server/internal/handlers/checkdependencies/service_test.go`:

- Create dependency: success, returns row.
- Create self-edge → `DEPENDENCY_SELF`.
- Create with parent in another org → `DEPENDENCY_CROSS_ORG`.
- Create cycle (A→B exists; create B→A) → `DEPENDENCY_CYCLE`.
- Create duplicate (A→B exists; create A→B) → `DEPENDENCY_DUPLICATE`.
- Cycle through three nodes (A→B→C exists; create C→A) → `DEPENDENCY_CYCLE`.
- PATCH kind from hard to soft → row updated; existing incidents unchanged.
- DELETE → row soft-deleted.

`server/internal/handlers/incidents/service_test.go` (extensions):

- **Hard parent down, child fails inside window** → child opens with
  `paging_suppressed = true`, `caused_by_incident_uid = parent.uid`. No
  notification job queued for the child.
- **Soft parent down, child fails** → child opens normally,
  `paging_suppressed = false`, notifications fire.
- **Hard parent down, child fails outside window** (e.g., parent went down
  20 min before child, period = 1min) → child opens normally.
- **Two hard parents both down**: A→C and B→C exist; A and B both have open
  incidents in the window. Pick the deeper one along the hard-path graph.
  If equally deep: oldest started_at wins.
- **Three-deep chain** (A→B→C, hard hard): A goes down, then C fails. C
  rolls up under A (the deepest hard ancestor reachable), not under B
  (which doesn't have an incident yet because B's check hasn't failed yet).
- **Parent resolves while child still down** → child's `paging_suppressed`
  flips to false; queueNotifications and escalation scheduling run *now*.
- **Parent resolves after child recovered** → child unaffected; no
  notification queue; emit `incident.rollup_detached` for the audit trail.
- **Depth cap**: build an artificial chain of 12 hard-linked checks. Failure
  at the bottom does not roll up under the top. Log a warning.
- **Escalation-suppression at fire time**: queue an escalation step,
  flip `paging_suppressed = true` after, fire the step → step exits without
  paging, emits `incident.escalation_skipped_suppressed`.

## Verification

Manual:
1. `make build && ./solidping migrate && make dev-test`.
2. Create three checks via the dashboard or CLI:
   - `rabbit` (TCP to localhost:5672)
   - `worker-a` (HTTP to a fake endpoint)
   - `worker-b` (HTTP to a different fake endpoint)
3. Add hard dependencies: `worker-a → rabbit`, `worker-b → rabbit`.
4. Stop the rabbit service. Within ~2 minutes:
   - `rabbit`'s incident opens normally; paging fires.
   - `worker-a`'s and `worker-b`'s incidents open with
     `paging_suppressed = true` and `caused_by_incident_uid = <rabbit-incident>`.
   - Inspect:
     ```sql
     select uid, check_uid, caused_by_incident_uid, paging_suppressed
     from incidents where state = 1;
     ```
5. Confirm no notification jobs were queued for `worker-a` / `worker-b`:
   pending jobs for those incidents should be zero.
6. On the dashboard, open the rabbit incident detail page. The "Blast
   radius" card lists worker-a and worker-b.
7. Restart rabbit. The rabbit incident resolves automatically. Re-evaluate:
   - If a worker is still failing: a notification job is queued *now*,
     and (if applicable) an escalation policy starts now.
   - If a worker has recovered: no-op, but
     `incident.rollup_detached` event appears in its timeline.
8. Switch one edge to soft (`worker-b → rabbit` becomes soft). Stop rabbit
   again. Verify worker-b's new incident opens with `paging_suppressed =
   false` and notifications fire.
9. API sanity: try to create a cycle (rabbit → worker-a) → 400 with
   `DEPENDENCY_CYCLE`.
10. Open the dependency list page (`/orgs/default/dependencies`) — the table
    matches what was created.

Automated: the test scenarios above run green via `make test`.

## Risks / unknowns to flag before coding

- **Correlation window default**. `max(2 * period, 5min)` is a guess. It's
  short enough to avoid false-attributing unrelated child failures, long
  enough to catch the typical fan-out delay. Validate against any production
  incident data you have before defaulting it. Worst case: bump it later
  in a one-line change.
- **Re-evaluation cost on parent resolve**. Walking N suppressed children
  inside the resolve transaction is fine for typical fan-outs (<50). If
  observed graphs go to hundreds of children, push the work to a job
  rather than blocking the resolve. Defer until measured.
- **Escalation worker fire-time check**. This is the one place where this
  spec *changes existing code* in a non-obvious way. Make the change
  alongside the rollup hook in the same PR — splitting it is asking for
  a regression where the suppression flag is set but escalation still
  pages.
- **Status-page implications** (future). A public status page reader
  probably wants one banner ("RabbitMQ degraded; 7 services affected")
  rather than 8 red lights. Out of scope here, but the data model
  (`caused_by_incident_uid`) is what a future status-page rollup will
  query. Don't paint into a corner.
- **Worker / heartbeat checks**. Heartbeat checks (passive: "the thing
  didn't ping us") can be parents — if a worker dies, every check it owns
  fails. The correlation window for a heartbeat parent should use the
  *heartbeat's* own period, which is typically longer than active checks.
  The `max(2 * period, 5min)` formula already handles this correctly;
  flag in code comments so it isn't lost.
- **Result-ingestion path performance**. The dependency walk runs on every
  incident open, which is hot during multi-check outages. The walk is
  a single indexed lookup per level (cap 10), so worst-case ~10 indexed
  reads. Profile at the same time as the spec is verified; if it shows up
  as a hotspot, cache parent sets per check (invalidate on dependency
  CRUD).
- **Vocabulary collision**. We already have `EscalationThreshold` on
  `Check` and `escalation_policies` on the paging side. "Cause-by" /
  "rollup" / "blast radius" are new vocabulary; resist the urge to call
  this "escalation" anywhere. Keep "escalation" reserved for paging
  ladders.
- **Incident-list filtering**. Operators will want a default that hides
  rolled-up incidents. Add a `?hideSuppressed=true` query param to the
  existing list endpoint and default the dashboard's incident list to
  `hideSuppressed=true`. A simple toggle reveals them. (One-line server
  change; the UI toggle is small.)

## Implementation Plan

The full surface area is large; deliver in discrete commits, each green:

1. **Migration 012** (PG + SQLite): create `check_dependencies` and add
   `caused_by_incident_uid` + `paging_suppressed` to `incidents`.
2. **Models**: `CheckDependency` model under `server/internal/db/models/`.
   Extend `Incident` with the two new fields and update
   `IncidentUpdate` accordingly.
3. **Repository**: a `CheckDependencyRepository` (CRUD + graph queries:
   parents-of, children-of, full org graph).
4. **Errors**: add new error codes to `base.HandlerBase`.
5. **Dependency service & handler** (`server/internal/handlers/checkdependencies/`):
   create/read/update/delete, cross-org guard, self-edge guard, cycle
   detection (DFS).
6. **Org graph endpoint**: `GET /api/v1/orgs/$org/dependencies`.
7. **Incident open hook**: BFS parents inside the incidents service,
   set rollup fields, skip notifications when suppressed. Use
   `DEPTH_CAP = 10`, `CORRELATION_WINDOW = max(2*period, 5min)`.
8. **Incident resolve hook**: re-evaluate suppressed children; flip
   `paging_suppressed` if still down and re-queue notifications.
9. **Escalation worker fire-time check**: refuse to page when the
   incident is suppressed.
10. **Incident-list filter**: `?hideSuppressed=true` query parameter.
11. **Tests**: service tests for dependency CRUD (happy + cycle/self/cross
    + duplicate), incident service tests for rollup behavior and
    re-evaluation. Skip exhaustive scenarios to keep the diff
    reviewable.
12. **Frontend (dash0)**: minimum viable — Blast radius card on the
    incident detail page, "Caused by" banner on suppressed incidents.
    Dependency picker on check edit, "Depends on / Depended on by"
    cards on check detail, and the org-wide list page can be a
    follow-up; cite this in the archived spec footer.
13. **QA**: `make build-backend build-client lint-back test`; iterate
    until green.

Keep commits granular: one per major step.
