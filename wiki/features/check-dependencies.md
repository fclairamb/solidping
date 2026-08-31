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

## The confirmation hold (prevention, not damage control)

The forward walk suppresses the pages that have not gone out yet — it cannot
recall the ones that have. During the RabbitMQ **nonprod** outage of
2026-08-30 that residue was 5 Slack reports instead of 1: parent and children
were configured identically (`period = 60s`,
`confirmationPeriodSeconds = 120`), the dependents' health endpoints flipped
to 503 within milliseconds of the broker dying, and the parent's own probe
only observed its first failure 26 s later (probe phase offset plus its ~15 s
connect timeout). Both sides then waited the *same* 120 s, so the 26 s gap was
preserved rather than closed, and four children paged before the parent
confirmed.

Note what this disproves: the tempting static fix
(`parent.period <= child.period` **and**
`parent.confirmation <= child.confirmation`) already held — everything was
equal — and pages still escaped. The invariant that actually closes the race
is a **strict margin**:

```
child.confirmation >= parent.confirmation + parent.period + parent.timeout
```

Enforcing that as configuration is a bad trade: it permanently slows every
child incident by the margin even when the parent is healthy, and it turns a
check edit into cross-resource validation (lowering one parent's timeout
invalidates every child). So the hold is **dynamic runtime state** instead
(spec `2026-08-31-06`):

> A check whose confirmation period has elapsed does **not** open an incident
> while a hard ancestor is itself in `validating` — it stays validating one
> more tick. When the ancestor confirms first, the child's eventual open is
> suppressed by the backward walk before anything is sent. One report: the
> parent's.

### How the gate is evaluated

`hardAncestorGatesConfirmation` (`rollup.go`) runs only on the rare pre-open
tick — a failing result, no active incident, and the confirmation window just
elapsed. Everything else returns before touching the database, so the
steady-state per-result cost is unchanged.

1. Walk parents BFS along **hard edges only**, same `visited` set and same
   depth cap of 10 as `findRollupRoot`. Load each level's ancestors with
   `GetChecksByUIDs`.
2. An ancestor **gates** iff all three hold:
   - `status == validating`, and
   - `first_failure_at` is set (defensive; validating implies armed), and
   - the per-ancestor **hold cap** has not expired:
     ```
     now < first_failure_at + confirmation + period + timeoutOrDefault
     ```
     where `timeoutOrDefault` is the check's own `timeout` config entry, or
     the server's `scheduling.check_timeout_ms` (default 15 s) when unset.
3. Any gating ancestor ⇒ the child stays `validating` for this result.
4. No gating ancestor ⇒ the open proceeds unchanged.

Three cases that **never** gate, and why:

- **Ancestor `down`** — its incident is already open, so the child should open
  now and be suppressed synchronously by the backward walk.
- **Ancestor `up`** — the child's failure is its own.
- **Soft edges** — never consulted, for rollup parity.

### The wedged-parent bound

The hold cap is what makes the gate safe without any extra bookkeeping. It is
the latest instant at which a healthy ancestor could still legitimately
confirm; past it the ancestor is treated as **wedged** — paused
mid-confirmation, frozen by a dead region, stuck behind a maintenance window —
and stops gating. So a stuck parent can delay a child by **at most one cap
window**, and no per-ancestor maintenance/paused queries are needed.

### Visible status agrees with the hold

`pickStatus` would otherwise flip the check to `down` the moment its
confirmation elapsed, rendering it as down with no incident behind it. The
gate is therefore computed **once**, in `ProcessCheckResult` (it needs DB
access; `pickStatus` is pure), and threaded as `holdConfirmation` through
both `deriveCheckStatus`/`pickStatus` and `handleFailure`. A held check reads
`validating`, and since `validating <-> down` never bumps `status_changed_at`
the hold costs no status churn.

The gate sits **before** `createOrReopenIncident`, so a reopen-after-cooldown
relapse is held by exactly the same rule as a first open.

Accounting is unaffected: `started_at` is already the *confirming* result's
`period_start` (not `first_failure_at`), so a held incident simply starts
later; downtime and availability come from results.

Every held evaluation logs at **INFO** with the child's uid/slug, the gating
ancestor's uid/slug and the ancestor's remaining cap — one line per child
period during a real outage, bounded by the cap, and it directly answers "why
did this page arrive 90 s late?". There are no new event rows: there is no
incident to attach them to yet.

`rollUpExistingChildren` stays as the safety net for what the gate cannot
see — edges created mid-outage, cap expiries, and children whose confirmation
elapsed before the edge existed.

### The soft config lint

`GET /orgs/:org/checks/:check/dependencies` returns a `warnings` array
alongside `dependsOn` / `dependedOnBy`. For each hard `dependsOn` edge where

```
child.confirmation < parent.confirmation + parent.period + parent.timeoutOrDefault
```

it emits `CONFIRMATION_MARGIN_TOO_SHORT`, carrying the child's configured
confirmation and the recommended value so the dashboard can render a concrete
sentence. dash0 shows it as an **amber** alert on the check's dependencies
card (`components/checks/dependency-warnings.tsx`).

It is **advisory only** — never a validation error, never a reason a write is
rejected. The runtime hold already covers the gap; the warning only explains
why a page may arrive later than the child's configured confirmation suggests.

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

- **Rollup never delays an open** — but the *confirmation hold* does, and
  only in the one case that matters. Once the child's confirmation elapses
  and no hard ancestor is validating, the incident is created immediately on
  the failing result; rollup is computed synchronously and the suppression
  flag is part of the same DB write. The hold adds latency **only** while a
  hard ancestor is itself mid-confirmation, and never beyond that ancestor's
  hold cap.
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
| Confirmation hold (gate + hold cap) | `rollup.go` (`hardAncestorGatesConfirmation`, `ancestorHoldRemaining`) |
| Hold threaded into status + open | `incidents/service.go` (`holdForValidatingAncestor`, `pickStatus`, `handleFailure`) |
| Resolved per-check timeout | `db/models/check.go` (`Check.TimeoutOrDefault`) |
| Confirmation-margin lint | `checkdependencies/service.go` (`confirmationMarginWarnings`) |
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
The forward walk was added by `2026-08-24-15` after the RabbitMQ outage of
2026-08-23; the confirmation hold and the soft margin lint by `2026-08-31-06`
after the nonprod outage of 2026-08-30 showed the forward walk to be damage
control rather than prevention.
