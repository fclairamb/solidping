---
model: opus
effort: high
---

# Replaced system agents pile up as duplicate rows until the 7-day GC — supersede them at enrollment

## Problem

A system agent that starts without a pinned identity (`SP_AGENT_KEYS` /
`SP_AGENT_KEYS_FILE`) generates a fresh keypair and enrolls as a brand-new
agent on every boot — that is the intended enroll-on-boot fleet design
(`server/internal/db/models/agent.go:115`). The price is that every machine
*replacement* (pod restart, redeploy, fly machine recreation) leaves the
previous agent row behind, and nothing retires it until the `agent_gc` job's
7-day silence window (`defaultAgentStaleAfter`,
`server/internal/jobs/jobtypes/job_agent_gc.go:25`).

Observed on production (`/dash0/orgs/default/server/agents`, 2026-08-28): the
`kansas-city-k8s` agent showed **seven** rows (six stale, one live) and the
Tokyo fly agent three, because neither deployment pinned its identity. The two
deployments have since been pinned operationally, but the platform behavior
remains: any self-hosted operator running an agent without a pinned identity
gets a duplicate row per restart, and the admin fleet list stays cluttered for
a week per incarnation.

A second, related defect: `agent_gc` drops its own configuration when it
reschedules itself. `rescheduleSelf`
(`server/internal/jobs/jobtypes/job_agent_gc.go:242`) calls
`CreateJob(ctx, "", JobTypeAgentGC, nil, …)` — config `nil` — so an operator
override of `StaleAfterSeconds` / `RevokedPurgeAfterSeconds` survives exactly
one sweep and then silently reverts to the defaults.

## Proposal

**1. Supersede-on-enroll for replaced system agents.** In the system-token
branch of the enrollment transaction
(`server/internal/db/postgres/agents.go:127`–`158`, mirrored in
`server/internal/db/sqlite/agents.go` — keep the two dialects in sync), after
inserting the new agent row: retire (revoke + soft-delete, same terminal state
as `RetireSystemAgent`) every *other* row matching all of:

- `kind = 'system'`, same `region`, same `name`, `status = 'active'`,
  `deleted_at IS NULL`;
- **provably not a live fleet peer**: `last_seen_at` is NULL or older than a
  conservative disconnect window (suggest 15 minutes — comfortably above the
  WS heartbeat/reconnect cadence). Live WS connection state is per-replica
  in-process, so `last_seen_at` staleness is the cross-replica proxy.

Rationale for the matching key: a same-name system agent reappearing in the
same region is a machine replacement, not a fleet peer. Fixed-name deployments
(k8s `SP_AGENT_NAME`) collapse to one live row within one restart instead of
seven in a week. Genuine fleets are safe on both axes: fly machines fall back
to `os.Hostname()` = unique machine IDs (never same-name), and a same-name
peer that is actually connected is protected by the `last_seen_at` guard.
**Org agents are never superseded** — they are customer-managed and offline ≠
replaced (same reasoning as the GC's org exclusion,
`job_agent_gc.go:89`).

Also clean up the superseded agents' `workers` rows the way the GC does
(`cleanupWorkerRow`, `job_agent_gc.go:203` — resolve by the deterministic
`agents.WorkerSlug(uid)`), so leases and result attribution don't linger.
Factor that helper so the enrollment path and the GC share it rather than
duplicating it.

**2. Preserve `agent_gc` config across self-reschedules.** `rescheduleSelf`
must marshal the run's `r.config` back into the created job instead of passing
`nil`, so operator overrides of `StaleAfterSeconds` /
`RevokedPurgeAfterSeconds` persist.

**Tests.**

- Enrollment supersede: same (region, name) stale predecessor is retired and
  its worker row soft-deleted; a same-name row with a fresh `last_seen_at`
  survives (fleet peer, positive control); a different-name same-region row
  survives; an org agent with the same name is never touched; both dialects.
- GC reschedule: a run created with a non-default config produces a follow-up
  job carrying the same config.

**Non-goals.** No UI change: with supersede in place the list is short-lived
truth again. Grouping duplicate rows client-side was considered and dropped —
it would only be masking state the server should not keep serving. The 7-day
GC stays as the backstop for fleets with unique per-machine names (fly), whose
replaced machines never match a newcomer's name.
