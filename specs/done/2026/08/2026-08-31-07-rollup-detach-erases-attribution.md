---
model: sonnet
effort: medium
---

# Rollup detach erases the cascade's audit trail and never emits its documented event

## Problem

When a rollup parent resolves, `reEvaluateRollupChildren` walks the
suppressed children and, for every child whose check has already recovered,
calls `markRollupDetached`
(`server/internal/handlers/incidents/rollup.go:386`), which clears **both**
`paging_suppressed` **and** `caused_by_incident_uid` — and emits nothing.

Consequences, all observed after the RabbitMQ nonprod outage of 2026-08-30
(UTC):

- **The cascade is unauditable after the fact.** Of the 11 dependent
  incidents (#477–#480 retro-suppressed by the forward walk, #482–#488
  suppressed at open), all but one now read
  `pagingSuppressed=false, causedByIncidentUid=null` — indistinguishable
  from "rollup never ran". The investigation had to be reconstructed from
  the events feed. The sole survivor, #487, kept its attribution only
  because it resolved *before* the parent, so
  `ListSuppressedChildIncidents` (which filters `state = active`) never
  revisited it.
- **The wiki documents behavior that doesn't exist.**
  `wiki/features/check-dependencies.md` says the recovered branch "emit[s] a
  `rollup_detached` event for the timeline" — no such event type exists
  (`server/internal/db/models/event.go` has only
  `EventTypeIncidentRolledUp`). It also claims suppression "skips the
  channel fan-out for everything except the resolved event", but
  `queueLifecycleNotifications` (`service.go:1346`) gates created,
  escalated, reopened **and resolved** alike.

Why the current clearing exists at all: a detached-recovered child whose
incident is still active (recovery window running) can relapse before it
resolves; with `paging_suppressed` cleared, that relapse pages. That
property must be preserved.

## Proposal

1. **Keep `caused_by_incident_uid` on detach.** `markRollupDetached` clears
   only `paging_suppressed` (drop `ClearCausedByIncidentUID: true`). The
   attribution is the post-mortem record; nothing downstream interprets
   `caused_by != nil` as "currently suppressed"
   (`ListSuppressedChildIncidents` requires `paging_suppressed = true`),
   and the reopen path already resets attribution from scratch on every
   reopen (`service.go:1237` — `tmp.CausedByIncidentUID = nil` +
   `applyRollup`, then `ClearCausedByIncidentUID` when no candidate), so no
   stale attribution can leak into a relapse.
2. **Emit the documented event.** Add
   `EventTypeIncidentRollupDetached = "incident.rollup_detached"` and emit
   it from the recovered branch with the parent incident/check uids in the
   payload, mirroring `incident.rolled_up`'s shape. Suppressed-child event
   emission is fine here: `emitEvent` records the timeline row regardless;
   `queueLifecycleNotifications`'s exhaustive switch must route the new
   type to "never pages".
3. **dash0 locale keys** for `incident.rollup_detached` in all six locales
   (`web/dash0/src/locales/*/events.json`), and the event-display mapping —
   `bun run test:unit` is the gate that catches a missing key.
4. **Fix the wiki** (`wiki/features/check-dependencies.md`):
   - detach now keeps the attribution and emits the event (make the
     existing claim true);
   - correct "everything except the resolved event" — suppression gates
     resolved paging too;
   - note that `causedByIncidentUid` on a resolved incident is a historical
     record, not a live suppression flag.

## Tests

Both dialects:

1. Parent resolves while child check recovered → child keeps
   `caused_by_incident_uid`, `paging_suppressed` flips false,
   `incident.rollup_detached` emitted, **no** notification queued.
2. Detached child relapses before its incident resolves → pages (the
   preserved property).
3. Still-down branch unchanged: keeps attribution, clears suppression,
   emits `incident.reopened`, pages.
4. Reopen of a previously-detached, resolved child with no failing parent →
   attribution cleared (fresh decision, existing behavior as regression
   guard).

## Out of scope

- The resolution-notice asymmetry: children #477–#480 paged their opens
  (pre-suppression race) and, when a child resolves while still suppressed,
  no resolved notice fires — recipients of the open never hear it ended on
  channels that don't update in place. Fixing that properly needs
  "did this incident ever page?" state; separate spec if it bites.
- Preventing the race that creates retro-suppressed children in the first
  place — spec `2026-08-31-06`.
