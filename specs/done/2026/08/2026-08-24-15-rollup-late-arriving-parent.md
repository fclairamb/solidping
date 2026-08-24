---
model: opus
effort: high
---

# Dependency rollup misses the common case where the parent confirms after its dependents — re-evaluate children when a parent incident opens

## Problem

Cascade rollup is evaluated exactly once, at child-incident creation, looking
**backward** only: `findRollupRoot`
(`server/internal/handlers/incidents/rollup.go:55-130`) searches for an active
hard-ancestor incident with `started_at` in
`[childStart - max(2×period, 5min), childStart]`. If the parent's incident
opens even a second after the child's, the child pages un-suppressed for its
whole lifetime. The only re-evaluation today runs when a parent **resolves**
(`reEvaluateRollupChildren`, `rollup.go:151`).

This is the dominant real-world ordering. During the RabbitMQ outage of
2026-08-23 (UTC), the dependents' health endpoints died at ~23:47:50 while the
core check (`rabbitmq-aws-prod`, polling the management console) only saw its
first failure at 23:48:05 and confirmed at 23:50:14 (120s confirmation). The 55
dependent incidents opened between 23:49:49 and 23:52:19 — most before the
parent confirmed, so every one of them paged. A core service's own probe
routinely outlives the consumers that depend on it; "parent confirms last" is
the norm, not the edge case.

Related gap found in the same investigation: `job_escalation_step.go:163-170`
re-checks only `AcknowledgedAt`/`ResolvedAt`/`SnoozedUntil` at fire time — not
`paging_suppressed` — even though the original spec
(`specs/done/2026/05/2026-05-03-57-check-dependencies-and-cascade-rollup.md:218-238`)
calls for it. Without that gate, retroactive suppression cannot stop an
already-scheduled escalation step.

## Proposal

1. **Forward re-evaluation on parent open.** When an incident is created or
   reopened for a check that has hard children
   (`ListCheckDependencyChildren`), walk hard descendants (same BFS shape and
   `rollupDepthCap` as `findRollupRoot`, downward) and, for each active,
   non-suppressed, `kind='check'` child incident whose `started_at` falls
   within the child's own correlation window **before** the parent's
   `started_at` (i.e. parent opened within `childStart + window`), set
   `caused_by_incident_uid` to the parent incident and
   `paging_suppressed = true`. Mirror image of the existing backward query.
2. **Escalation fire-time gate.** Add `paging_suppressed` to the early-exit
   checks in `server/internal/jobs/jobtypes/job_escalation_step.go:163-170` so
   pending escalation steps of a retroactively suppressed child stop firing.
   (Notifications already sent are gone — accepted; the goal is stopping the
   remaining storm, not recalling it.)
3. **Observability.** Emit a lifecycle event when a child is retroactively
   rolled up (the existing event vocabulary in
   `server/internal/handlers/incidents/service.go` — reuse or add an
   `incident.rolled_up`-style type), and log at INFO, not DEBUG: today the
   only success log for rollup is DEBUG (`rollup.go:44`), which made the
   2026-08-23 investigation needlessly hard.
4. **Idempotence and races.** Parent open and child open can race from two
   workers; both sides evaluating (backward at child-create, forward at
   parent-create) must converge on the same attachment without double events.
   Same-transaction updates or a guard on `paging_suppressed = false` in the
   UPDATE's WHERE clause is sufficient.
5. **Tests.** `rollup_test.go` currently covers only `correlationWindow` and
   `derefSlug`. Cover at minimum: child-before-parent (this spec's case),
   parent-before-child (existing path still works), both-in-same-second race,
   depth > 1 chains, soft edges ignored, escalation step skipped when
   suppressed, and the parent-resolve re-evaluation still detaching recovered
   children.

Note: with companion spec 2026-08-24-14 (per-check incidents for grouped
checks), the parent here gets a real per-check incident, which is what makes
this spec effective for grouped cores like `rabbitmq-aws-prod`. The two specs
are independent to implement but complementary; 14 first is the natural order.

## Implementation Plan

### 1. Forward re-evaluation on parent open (`rollup.go`)

New `rollUpExistingChildren(ctx, parentCheck, parent, onset)` — the downward
mirror of `findRollupRoot`:

- BFS over `ListCheckDependencyChildren`, hard edges only, `visited` map,
  same `rollupDepthCap`.
- Per BFS level: load the hard children with `GetChecksByUIDs` (one round-trip),
  group them by `correlationWindow(child)` — the window is a property of the
  **child**, so children with different periods need different bounds — and
  call the **existing** `FindActiveIncidentsForChecksInWindow(uids,
  onset - window, onset)` once per distinct window. That is literally the same
  query the backward path uses, with `until = parentStart` instead of
  `until = childStart`: it already filters `kind = 'check'`, `state = active`,
  `paging_suppressed = false`, `deleted_at IS NULL`.
- Group ordering is made deterministic (first-appearance order), not map order.
- Called from `createIncident` (after the row exists — `caused_by_incident_uid`
  is an FK) and from `reopenIncident` (with `onset = result.PeriodStart`, since
  a reopened incident keeps its original `started_at`).

### 2. Idempotence / race guard (new DB method)

`AttachIncidentToRollupParent(ctx, childIncidentUID, parentIncidentUID) (bool, error)`
in `db.Service` + postgres + sqlite: a compare-and-set

```
UPDATE incidents SET caused_by_incident_uid = ?, paging_suppressed = TRUE, updated_at = now
WHERE uid = ? AND paging_suppressed = FALSE AND state = 'active' AND deleted_at IS NULL
```

returning `rowsAffected > 0`. The `paging_suppressed = FALSE` guard is what makes
the forward and backward paths converge without a double event: whoever loses the
race updates 0 rows and emits nothing. Self-attachment is skipped in Go.

### 3. Escalation fire-time gate

`incidentNeedsPaging` (`job_escalation_step.go`) gains
`if incident.PagingSuppressed { return false }`, so an escalation step already
queued for a child that got retroactively suppressed stops firing.

### 4. Observability

- New `models.EventTypeIncidentRolledUp = "incident.rolled_up"`, emitted on the
  **child** incident with the parent incident/check identity and the depth in
  the payload. Registered in both exhaustive `EventType` switches
  (`incidents.queueLifecycleNotifications` — non-paging; `system.applyActivationEvent`),
  given a label in all four dash0 locales and an `INTENTIONALLY_UNMAPPED` entry
  (the `incident.*` family fallback supplies tone + icon), and documented in
  `wiki/features/notifications-and-escalation.md`.
- `rollup.go:44` DEBUG success log becomes INFO, and the new forward path logs
  at INFO too.

### 5. Tests

`rollup_forward_test.go` (package `incidents_test`), built on the spec-14
sqlite + `clock.Fake` harness so every correlation-window assertion is
deterministic:

1. child-before-parent — the headline. Genuine positive control: with the
   backward-only code the child incident opens while the parent is still up, so
   nothing ever revisits it and `paging_suppressed` stays false.
2. parent-before-child — the backward path still suppresses (regression guard).
3. same-instant open — inclusive bounds on both sides, exactly one attachment,
   exactly one `incident.rolled_up` event.
4. depth > 1 chain (A → B → C), all hard.
5. soft edge ignored.
6. out-of-window child left alone (window boundary).
7. `incidentNeedsPaging` returns false for a suppressed incident
   (`job_escalation_step_suppressed_test.go`, package `jobtypes`).
8. parent resolve still detaches a recovered child and un-suppresses one that
   is still down.
