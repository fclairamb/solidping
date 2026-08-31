---
model: opus
effort: high
---

# A child's confirmation must not finish while a hard parent is still validating — gate the open dynamically instead of racing the rollup

## Problem

The forward rollup (spec `2026-08-24-15`) is damage control, not prevention: it
suppresses the *remaining* pages after a late-confirming parent opens, but it
cannot recall the pages already sent. During the RabbitMQ nonprod outage of
2026-08-30 (UTC) that residue was 5 Slack reports instead of 1:

- 12 incidents opened (#477–#488), all with `period=60s`,
  `confirmationPeriodSeconds=120` — parent and children identical.
- The dependents' health endpoints flipped to 503 within milliseconds of the
  broker dying; the parent probe (`rabbitmq-aws-nonprod`, #481) observed its
  first failure up to `period + timeout` later (probe phase offset + its
  ~15s connect timeout — `sp.check_timeout_ms` default is 15000,
  `server/internal/config/config.go:1458`). Observed gap: 26 s.
- Both sides then waited the same 120 s confirmation, so the gap was
  *preserved*, not closed: children #477–#480 confirmed 23:24:05–23:24:23,
  the parent at 23:24:31. Four `incident.created` pages were already out.
- The forward walk fired correctly at 23:24:31.620–.629 (4 ×
  `incident.rolled_up`, depth 1) and the backward walk suppressed the 7
  children that confirmed after the parent (#482–#488, only #487 still shows
  it — see spec `2026-08-31-07`). Mechanism healthy; ordering lost.

The tempting static fix — enforce `parent.period <= child.period` and
`parent.confirmation <= child.confirmation` — is disproven by this very
outage: both inequalities already held (everything equal) and 5 pages
escaped. The invariant that actually closes the race is a **strict margin**:

```
child.confirmation >= parent.confirmation + parent.period + parent.timeout
```

Enforcing that as config is a bad trade: it permanently slows every child
incident by the margin even when the parent is healthy, and it turns check
edits into cross-resource validation (lowering one parent's timeout
invalidates every child; IaC `dependsOn` apply order becomes a footgun).

## Proposal

Make the hold **dynamic runtime state**, not config:

> A check whose confirmation period has elapsed does **not** open an incident
> while a hard ancestor is itself in `validating` — it stays validating one
> more tick. When the ancestor confirms first, the child's eventual open is
> suppressed by the existing backward walk (`applyRollup`) before anything is
> sent. One report: the parent's.

Properties:

- **Zero cost in the healthy case.** A child failing alone (all hard parents
  up) confirms at exactly its configured period — no permanent latency tax,
  which the static margin cannot offer.
- **No new machinery.** No timers, no recall, no schema change, no config
  knob. The gate is recomputed from live check rows on every failing result;
  the suppression path is the already-shipped backward walk.
- **Self-tuning.** It doesn't care whether the parent's lag came from phase,
  timeout, region count, or a slow prober — everything a config formula has
  to over-approximate.

### Gate semantics

Evaluated only on the rare pre-open tick — `isFailure`, no active incident,
`confirmationElapsed` just true (`handleFailure`,
`server/internal/handlers/incidents/service.go:885`):

1. Walk hard ancestors, BFS over `ListCheckDependencyParents`, hard edges
   only, same `visited` set and `rollupDepthCap` (10) as `findRollupRoot`
   (`rollup.go:237`). Load the ancestor check rows via `GetChecksByUIDs`.
2. An ancestor **gates** iff:
   - `Status == CheckStatusValidating`, and
   - `FirstFailureAt != nil` (defensive; validating implies armed), and
   - the per-ancestor **hold cap** hasn't expired:
     `now < FirstFailureAt + ConfirmationPeriod + Period + resolvedTimeout`
     (timeout falling back to the worker default when unset). Past the cap
     the ancestor is treated as wedged and stops gating — a paused,
     in-maintenance, or dead-region parent frozen in validating can hold a
     child for at most one cap window, so no per-ancestor maintenance/paused
     queries are needed.
3. Any gating ancestor ⇒ the child **stays validating** for this result
   (`return nil`, exactly like the confirmation-window branch above it).
4. No gating ancestor ⇒ proceed to `createOrReopenIncident` unchanged.
   - Ancestor `Down` never gates: its incident is open, so the child opens
     now and the backward walk suppresses it synchronously.
   - Ancestor `Up` never gates: the child's failure is its own.
   - Soft edges are never consulted (rollup parity).

The gate sits **before** `createOrReopenIncident`, so reopen-after-cooldown
relapses are covered by the same hold.

### Visible status must agree

`pickStatus` (`service.go:818`) flips the visible status to `down` the moment
`confirmationElapsedDerive` passes — with the gate holding the open, that
would display `down` with no incident. Compute the gate decision **once** in
`ProcessCheckResult` (it needs DB access; `pickStatus` is pure) and pass a
`holdConfirmation bool` into both the derive chain and `handleFailure`, so
the check visibly remains `validating` while held. `validating <-> down`
transitions don't bump `statusChangedAt`, so no churn there.

Note on accounting: `startedAt` is already the *confirming* result's
`PeriodStart` (not `FirstFailureAt`), so a held incident starting later is
consistent with existing semantics; downtime/availability come from results
and are unaffected.

### Config lint (soft, never enforced)

Surface the strict-margin formula as a warning where dependencies are
managed:

- `GET /checks/$check/dependencies`
  (`server/internal/handlers/checkdependencies/`): for each hard `dependsOn`
  edge where
  `child.confirmation < parent.confirmation + parent.period + parent.timeoutOrDefault`,
  add a `warnings` entry ("this check can confirm before its hard parent can
  possibly detect the same outage; the confirmation hold will cover the gap
  at page time").
- dash0: render it as an amber warning on the dependencies view
  (`web/dash0/src/routes/orgs/$org/dependencies.index.tsx` and/or the
  check's dependencies tab). Start from the design reference; amber warning
  style, never a blocking validation. New strings need locale keys in **all
  six locales** and `bun run test:unit` must pass.

### Observability

- INFO log every held evaluation: child uid/slug, gating ancestor uid/slug,
  ancestor's remaining cap. One line per child period during a real outage —
  bounded by the cap, and it directly answers "why did this page 90 s late?".
- The hold is user-visible as a longer `validating` phase; no new event rows
  (there is no incident to attach them to yet).

### What stays

`rollUpExistingChildren` (forward walk) remains untouched as the safety net
for what the gate cannot see: edges created mid-outage, cap expiries, and
children whose confirmation elapsed before the edge existed.

## Implementation plan

1. `server/internal/handlers/incidents/rollup.go`: add
   `hardAncestorGatesConfirmation(ctx, check, now) bool` — the BFS + cap
   check above. Reuses `ListCheckDependencyParents`, `GetChecksByUIDs`,
   `rollupDepthCap`; needs the resolved default timeout (plumb
   `sp.check_timeout_ms` into the incidents service config, mirroring how
   the worker resolves it in `configWithDefaultTimeout`,
   `checkworker/worker.go:1133`).
2. `service.go`: compute the gate once in `ProcessCheckResult` (only when
   `isFailure && activeIncident == nil && confirmationElapsed`), thread it
   through `deriveCheckStatus`/`pickStatus` and `handleFailure`.
3. Lint: warnings in the checkdependencies GET handler; amber display in
   dash0; locale keys ×6.
4. Wiki: extend `wiki/features/check-dependencies.md` with the gate (new
   section alongside the forward walk), including the cap formula and the
   wedged-parent bound.

## Tests

Table-driven, both dialects (PG via testcontainers + SQLite), using the
service clock:

1. **Core**: parent validating at child-confirmation time → child stays
   validating, no incident; parent confirms → parent pages; child's next
   failing result opens **suppressed** via backward walk → exactly one paged
   incident. Assert on emitted events, not just rows.
2. **Positive control**: same topology, parent healthy → child opens and
   pages at exactly its own confirmation (proves the gate doesn't fire; a
   green run must be able to fail).
3. **Parent blip**: parent recovers during the hold, child keeps failing →
   gate releases, child opens un-suppressed and pages.
4. **Cap expiry**: parent frozen in validating past
   `FirstFailureAt + conf + period + timeout` → child opens un-suppressed.
5. **Chain**: `db → api → child`, db validating → both api and child hold;
   db confirms → api opens suppressed, child opens suppressed → one page.
6. **Soft-only parent** and **no parents**: behavior byte-identical to
   today.
7. **Reopen**: child in reopen-cooldown relapses while parent validating →
   reopen is held, then suppressed once parent confirms.
8. **Status coherence**: while held, check status stays `validating`
   (never `down`-without-incident).
9. Lint: warning present/absent around the margin boundary; timeout default
   substitution.

## Out of scope

- Recalling already-sent notifications (impossible for most channels).
- The detach/attribution fixes — spec `2026-08-31-07`.
- Group incidents (`2026-04-29-04`) — incidents stay per-check
  (spec `2026-08-24-14`).
- Hard enforcement of any config inequality between parents and children.
