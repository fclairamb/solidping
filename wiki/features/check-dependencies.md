# Check dependencies & cascade rollup

How "the database is down" stops becoming "the database is down AND the
API is down AND the dashboard is down AND the worker is down" in your
on-call queue.

A **dependency edge** says "if check A fails, check B is *expected* to
fail too" — A is the parent, B is the child, and the edge has a kind:

- **hard**: failure of A *causes* failure of B. The failure of B is
  redundant information. When B opens an incident while A's incident is
  open, B's incident is marked `pagingSuppressed = true` and channels
  don't fire for B. B's incident still exists in the timeline; it just
  doesn't page.
- **soft**: A and B are correlated but neither causes the other. The
  incident still pages.

This page covers what the cascade rollup actually does at incident-open
and parent-resolve time. For the data model and CRUD API surface see
[api-specification/checks.md](../api-specification/checks.md). For the broader
notification pipeline see [notifications-and-escalation.md](notifications-and-escalation.md).

## When rollup applies

At every `incidentCreated`, the service runs `applyRollup`
([`server/internal/handlers/incidents/rollup.go:26`](../../server/internal/handlers/incidents/rollup.go)):

1. Walk parents BFS along **hard edges only**. Soft edges are ignored
   for cascade purposes — they're informational.
2. At each ancestor, query for open non-suppressed incidents inside the
   **correlation window**: `max(2 × child.period, 5 minutes)`. The
   window is centered on the child incident's `startedAt` and looks
   back, not forward.
3. Pick the **deepest** ancestor that has an in-window incident. Ties
   break on oldest `startedAt`.
4. If found: set `child.causedByIncidentUid = parent.uid` and
   `child.pagingSuppressed = true`. The child's incident row exists,
   but `emitEvent` (`incidents/service.go:888`) skips the channel
   fan-out for everything except the resolved event.

The depth cap is 10. In practice that's plenty for any sane org topology;
a real graph that hits the cap probably has a cycle or a confused
modeling decision and the cap is a safety net rather than a real limit.

## When the parent confirms LAST (the common case)

The backward walk above only fires at the child's incident-open, so on its
own it misses the ordering that dominates in production: a core service's
own probe usually confirms **after** the consumers that depend on it. During
the RabbitMQ outage of 2026-08-23 the dependents' health endpoints died at
23:47:50, the core check only confirmed at 23:50:14, and all 55 dependent
incidents had already opened un-suppressed — every one of them paged.

So an incident open (or reopen) also runs the **forward** mirror,
`rollUpExistingChildren` (same file):

1. Walk children BFS along **hard edges only**, same `visited` set and same
   depth cap of 10.
2. For each hard descendant, query its open, non-suppressed, `kind = check`
   incidents inside **the child's own** correlation window — the same
   `max(2 × child.period, 5 minutes)` rule and literally the same query as
   the backward path, with `until = the parent's onset` instead of
   `until = the child's onset`. Children with different periods are grouped
   by window so each gets its own bounds.
3. Attach each match with a compare-and-set
   (`AttachIncidentToRollupParent`): the UPDATE carries
   `WHERE paging_suppressed = FALSE AND state = 'active'`, so a forward walk
   racing the child's own backward evaluation converges on exactly one
   attachment and exactly one event.
4. Emit `incident.rolled_up` on the child, and log the attachment at
   **INFO** — "why did this incident not page?" is the first post-mortem
   question and it used to be answerable only at DEBUG level.

Because the attachment can land *after* the child's escalation cycle was
scheduled, `incidentNeedsPaging`
(`jobs/jobtypes/job_escalation_step.go`) re-checks `pagingSuppressed` at
**fire time**, alongside acked / resolved / snoozed. Pages already delivered
are not recalled — the goal is stopping the rest of the storm.

## Why "deepest" instead of "first hard parent"

Imagine: `db → api → dashboard → status-page`. All four edges are hard.
If `db` fails, all four start failing within a minute. Picking the
**deepest** in-window parent (`db`) means every child rolls up under
`db` directly, not in a chain. The on-call sees one incident
("db is down") instead of a four-incident pyramid where each one points
to the parent above it.

The "deepest" choice also handles the realistic case where one
intermediate node is *also* a single point of failure for an unrelated
service — the rollup walks past it because its incident isn't
in-window for our specific child.

## What happens when the parent resolves

Parent recovery triggers `reEvaluateRollupChildren`
(`rollup.go:151`). For each child whose `causedByIncidentUid` points to
the resolving parent:

- If the child's check has recovered (status flipped back to UP):
  emit a `rollup_detached` event for the timeline, clear the
  attribution, and don't page. The child resolved silently along with
  the parent — there was never a real on-call moment for the child's
  failure mode.
- If the child is still failing on its own: clear `pagingSuppressed`,
  emit an `incidentReopened` event. This is when the channel fan-out
  finally happens for the child. The on-call learns "the underlying db
  recovered, but here's an actually-different problem now visible".

This second branch is the load-bearing reason cascade rollup doesn't
make problems disappear: a child that's down for *its own* reason still
pages, just with a delay from the open time until the parent resolves.

## What happens on reopen

The service also re-evaluates rollup on `reopenIncident`
(`incidents/service.go:413`). A relapse of the child during the
parent's still-failing window will pick up the suppression again. The
relapse is treated as a fresh decision; it doesn't inherit the
`pagingSuppressed` state from the previous (resolved) instance of the
child incident.

## Soft edges

A soft edge has the same parent/child shape and the same UI affordances,
but it's intentionally **not** consulted by the rollup walk. The use
case is documentation: "these two checks are related; understand them
together when triaging" without changing paging behavior. Soft edges
also feed the dependency-graph visualization (the dashboard's
`/dependencies` page) and can be useful for correlation queries by
hand.

## Dependencies declared in check config

Two ways to add a dependency:

1. **API surface** — `PUT /api/v1/orgs/$org/checks/$check/dependencies`
   manages the explicit edges stored in `check_dependencies`.
2. **Inline `dependsOn` in check config** — convenience for IaC users.
   The check's `config.dependsOn` is read on create/update and
   reconciled into `check_dependencies` rows. The DB layer is the
   source of truth; `dependsOn` in config is a *declaration*, not a
   second store. Edits made via the API surface and edits made via
   `dependsOn` converge.

## Operational caveats

- **Rollup never delays an open**. The child incident is created
  immediately on the failing result; rollup is computed synchronously
  and the suppression flag is part of the same DB write. There is no
  "waiting to see if a parent fails first" backoff.
- **Rollup is evaluated in both directions, at every incident open and
  reopen** (spec `2026-08-24-15`). Backward at the child's open, forward
  at the parent's — so a parent that confirms after its dependents still
  suppresses them retroactively, provided its onset lands inside each
  child's own correlation window. Outside that window nothing is
  reattached: a child that has been down for an hour is not somebody
  else's cascade.
- **Retroactive suppression does not recall notifications already sent.**
  It stops the remaining escalation steps (fire-time `pagingSuppressed`
  gate) and every later lifecycle notification for that child, nothing
  more.
- **The correlation window is per-child** based on the child's check
  period. A child polled every 30s gets a 5-minute window (the floor);
  a child polled every 10 minutes gets a 20-minute window. This is
  intentional — slow-polling children might genuinely be a few minutes
  behind their fast-polling parent.
- **Maintenance windows compose**. A child in maintenance gets no
  incident at all (the suppression in `ProcessCheckResult` short-circuits
  before the state machine), regardless of whether a hard parent is
  also failing. Rollup never runs for in-maintenance checks.
- **Cycles are blocked at edge create time** via the `INVALID_CYCLE`
  error code — `applyRollup` doesn't need cycle detection at runtime
  beyond the `visited` set in the BFS.

## Where to look in the code

| Concern | File |
|---|---|
| Rollup walk + suppression | [`server/internal/handlers/incidents/rollup.go`](../../server/internal/handlers/incidents/rollup.go) |
| Forward walk on parent open/reopen | `rollup.go` (`rollUpExistingChildren`) |
| Guarded attachment (race convergence) | `db/{postgres,sqlite}/check_dependencies.go` (`AttachIncidentToRollupParent`) |
| Escalation fire-time suppression gate | `jobs/jobtypes/job_escalation_step.go` (`incidentNeedsPaging`) |
| Parent-resolve re-evaluation | `rollup.go:151` (`reEvaluateRollupChildren`) |
| Suppressed-child notification skip | `incidents/service.go:888` |
| Edge CRUD handlers | `server/internal/handlers/checkdependencies/` |
| Cycle detection on edge create | same package, `service.go` (`INVALID_CYCLE` error) |
| Reopen re-evaluation | `incidents/service.go:413` |

## Origin

The cascade-rollup feature shipped in May 2026; the design rationale and
edge cases are captured in
[`specs/done/2026/05/2026-05-03-57-check-dependencies-and-cascade-rollup.md`](../../specs/done/2026/05/2026-05-03-57-check-dependencies-and-cascade-rollup.md).
The frontend dashboard work followed in
[`2026-05-05-01-check-dependencies-frontend.md`](../../specs/done/2026/05/2026-05-05-01-check-dependencies-frontend.md).
