---
model: opus
effort: high
---

# A private location's agent can go offline and nobody is told, although the org is the only one who can fix it

## Problem

An org can run its own agents in a private location (region `@<slug>`, scoped by
`organization_uid`, [regions.go:41-60](server/internal/regions/regions.go#L41)).
When that agent dies, every check pinned to the location silently stops. It is
the same failure as the 2026-09-24 `lauterbourg` outage, but on the customer's
own machine:

- **There is no disconnect signal.**
  - `last_seen_at` is refreshed on connect, on each 25 s ping, on each claim
    and on each result
    ([agentws/handler.go:641, 728-748, 897, 1194](server/internal/handlers/agentws/handler.go#L641)).
  - On disconnect, only the in-memory registry entry is removed. No event, no
    audit entry, no database write: `last_seen_at` just stops moving.
- **Nothing tells the org.**
  - The Private Locations page shows "last seen X ago" but no online/offline
    state, no status and no link to anything
    (`organization.private-locations.index.tsx:71-81, 392`).
  - Only the super-admin Agents page flags an agent stale after 5 minutes, and
    it computes that in the browser.
  - The agent GC job deliberately leaves org agents alone
    ([job_agent_gc.go:88-90](server/internal/jobs/jobtypes/job_agent_gc.go#L88)).
  - The platform watchdog is operator-only. It also cannot see private regions
    correctly yet (spec `2026-09-25-01`).
- **The operator cannot fix a customer's machine.** Region-level platform
  alerting (spec `2026-09-25-03`) deliberately leaves private regions out. The
  org owns that agent, so the org is who must be paged, through its own
  escalation policy, integrations, maintenance windows and status pages.

## Proposal

### 1. A liveness monitor that behaves like any check

Add a passive check type, **`private-location`**:

- Config: `{ "region": "@<slug>" }`.
- Evaluated on the jobs node by the passive evaluator from spec
  `2026-09-25-04`, never inside the private region it watches.
- Evaluation reuses the per-(org, `@slug`) liveness from spec `2026-09-25-01`,
  based on `agents.last_seen_at` and `WorkerLivenessWindow` (5 minutes,
  [capabilities.go:54](server/internal/regions/capabilities.go#L54)):

| Situation | Status | Output |
|---|---|---|
| every active agent seen within the window | Up | "2 agents connected" |
| some active agents seen, some not | Warning | "1 of 2 agents offline: `office-2` last seen 13:41" |
| no active agent seen within the window | Down | "No agent connected since 13:41" (newest `last_seen_at`) |
| no active agent enrolled yet | Created (no incident) | "No agent enrolled" |

Because it is a real check, it gets everything checks already have:

- incidents with confirmation and recovery periods;
- escalation policies and default integrations (copied on create like any
  check, `checks/service.go:1590`);
- maintenance windows (planned agent upgrades), status page resources, SLOs,
  history.

It also gets no special case in those code paths.

Default period: 1 minute. The evaluation is a single indexed read, and the
5-minute liveness window already absorbs reconnect blips (the agent retries
from 5 s up to 60 s, `backend/ws.go:60-65`).

### 2. Created and removed with the location

- **Create:** in `CreatePrivateRegion`
  ([agents/service.go:150-195](server/internal/handlers/agents/service.go#L150)).
  Name it "Private location: <name>", slug `private-location-<slug>`.
- **Remove:** in `DeletePrivateRegion` (`:200-240`), which already refuses to
  run while active agents exist.
- **Backfill:** at startup (`job_startup.go`), create the monitor for every
  existing private region that has none. Also re-ensure it idempotently after
  `EnrollAgent` (`agentws/handler.go:283`).
- **Opting out:** a user may disable the monitor, or delete it. Deleting
  records the opt-out on the private region so the backfill does not recreate
  it. The region page shows "liveness monitor off" with a one-click re-enable.
- **Quota:** it does not count against `maxChecks` or `maxChecksPerMinute`. It
  costs one read, and charging for being told your agent is down would push
  orgs to switch it off.

### 3. Record disconnects

On socket close, write an org-scoped `agent.disconnected` event with the close
reason: ping timeout, revoked, server shutdown, or error. On a later connect,
write `agent.connected`. It is best effort and never blocks the connection path.
The monitor's Down output links the last disconnect reason when there is one.

### 4. Private Locations page

- Per agent: an online/offline badge from the same liveness rule, next to
  "last seen".
- Per location: overall state and a link to its liveness monitor.
- Use the design reference primitives. Add every string to every locale
  (`en`, `fr`, `de`, `es`; a parity test checks them).

### 5. Registering the type

Follow the end-to-end list:

- server: `checkerdef/types.go` (constant, metadata table, `ListCheckTypes`),
  `registry/registry.go`, `configregistry/configregistry.go`, then regenerate
  the JSON schemas (`go generate ./internal/checkers/schemas/...`);
- docs: a `### … {#anchor}` section in
  `web/docs/docs/features/check-types.md` plus its entry in
  `check-type-docs-anchors.ts` (enforced by `docs_anchor_test.go`);
- dash0: type lists (`form/types/common.ts`, `api/hooks.ts`, `check-form.tsx`),
  form module registry, `check-type-identity.tsx`, locales;
- OpenAPI: the check-type enum, which is already stale
  (`openapi.yaml:9902, 10335, 10593`). Fix it while there;
- MCP: `list_check_types` picks the type up from the metadata table. Fix the
  hard-coded "Allowed:" lists in `mcp/tools_checks.go:164` and
  `mcp/tools_results.go:48`.

The type is created by the system. The form offers it read-only (edit period,
escalation and integrations, not the region). It cannot be pointed at a region
the org does not own.

### Tests

- Creating a private region creates its monitor. Deleting the region deletes
  it. The backfill creates exactly one, and none after an opt-out.
- Agent seen 30 s ago: Up. One of two stale: Warning. All stale for more than
  5 min: Down, and an incident opens after the confirmation period.
  Reconnecting resolves it.
- No agent enrolled: Created, no incident.
- The evaluation keeps working with every cloud worker and every agent stopped
  (jobs node only).
- An org cannot create a `private-location` check for another org's `@slug`.
- The monitor does not count toward quota.
- Disconnect writes one `agent.disconnected` event with the reason.

## Dependencies

- `2026-09-25-01`: per-org private-region liveness (fixes the NULL-region
  worker rows). This spec reuses its calculation.
- `2026-09-25-04`: passive evaluation on the jobs node.

## Out of scope

- Platform-operated (system) agents serving shared cloud regions. They are the
  operator's problem and are covered by spec `2026-09-25-03`.

## Implementation Plan

### Shared liveness rule (reuse of spec 01)
- Extract the per-agent rule `agentCoverageForKey` applies (status `active` AND
  `last_seen_at >= now - regions.WorkerLivenessWindow`, inclusive) into
  `regions.IsAgentLive(status, lastSeenAt, liveCutoff)` + `regions.LivenessCutoff(now)`.
- `handlers/checks/region_health.go` `agentCoverageForKey` calls it (no behaviour change),
  the new evaluator calls it, and the Private Locations API calls it for the per-agent
  `online` flag. One rule, three readers.

### §1 Check type + evaluation (plugged into spec 04's passive machinery)
- `checkerdef.CheckTypePrivateLocation = "private-location"`; `IsPassive()` and
  `PassiveCheckTypes()` include it, so it automatically gets: one NULL-region job
  (`Check.JobRegions`), exclusion from cloud-worker and agent claims, the jobs node's
  `PassiveEvaluator` as its only claimer, no region picker, exemption from
  `maxChecksPerMinute` (demand + gate already skip `IsPassive`).
- `checkprivatelocation` package (+ light `config` sub-package): config `{region: "@slug"}`,
  offline validation of the shape; `Execute` returns ErrNotExecutable like heartbeat.
- Dispatch: `passiveVerdict` (shared by `PassiveEvaluator.evaluate` and the worker's
  defensive branch) switches on the job type; `private-location` goes to a new
  `privateLocationVerdict`, which reads agents through an optional
  `backend.PrivateLocationReader` interface (implemented by `DirectBackend` = the jobs
  node; `WSBackend` does not implement it, so an agent can never evaluate it).
- Verdict table (pure function `privateLocationEvaluation`, table-tested):
  - active agents all live -> Up "N agents connected";
  - some live, some not -> Warning "1 of 2 agents offline: office-2 last seen 13:41 UTC";
  - none live -> Down "No agent connected since 13:41 UTC" (newest last_seen_at of any
    agent bound to the location, revoked included, as spec 01 does), plus
    `lastDisconnectReason` / `lastDisconnectAt` / `lastDisconnectEventUid` from the newest
    `agent.disconnected` event of that location;
  - no active agent AND the check has never produced a result -> grace (write nothing,
    move the schedule): the check stays `created`, no incident. The freshness sweep
    exempts exactly this state (`private-location` + status `created`) in SQL on both
    engines and in `Check.IsDataStale`, otherwise a location with no agent would go
    stale after 5 min. Once the location had a result, losing every agent is Down.
- Default period 1 min (type metadata `DefaultPeriod`).

### §2 Lifecycle
- `checks.Service` gets `EnsurePrivateLocationMonitor(ctx, orgUID, slug)`,
  `RemovePrivateLocationMonitor(ctx, orgUID, slug)`, `BackfillPrivateLocationMonitors(ctx)`
  and `EnablePrivateLocationMonitor(ctx, orgSlug, slug)` (re-enable). Creation goes through
  the normal `CreateCheck` path (default integrations, check.created event, status page
  selectors) with name "Private location: <name>", slug `private-location-<slug>`.
  A monitor is found by (org, type, `config.region`).
- Opt-out stored on the region definition (`RegionDefinition.LivenessMonitorOff`,
  persisted in `custom_regions`). `DeleteCheck` of a private-location check sets it;
  `EnsurePrivateLocationMonitor` (backfill / enroll) does nothing while it is set;
  re-enable clears it, re-creates or re-enables the monitor.
- `agents.Service` gets an optional `PrivateLocationMonitors` hook: called after
  `CreatePrivateRegion` (ensure) and `DeletePrivateRegion` (remove, no opt-out).
  `agentws.Handler` gets the same optional hook, called (best effort) after `EnrollAgent`.
- Startup job: `backfillPrivateLocationMonitors` via a new `services.Registry` field
  (`PrivateLocationMonitors`, an interface), best effort.
- Quota: `MaxChecks` usage skips `private-location` rows
  (`checkerdef.CheckType.IsQuotaExempt`), and `planCreateCheck` skips the MaxChecks gate for
  that type; `maxChecksPerMinute` is already skipped via `IsPassive`.
- Ownership: a new config validator in `configValidationErrors` rejects a
  `private-location` check whose `config.region` is not one of the org's own private
  regions (create, update, validate, import all run it). Region is immutable on update.

### §3 Disconnect events
- `serveConnEvents` returns a close reason (`ping_timeout`, `revoked`, `server_shutdown`,
  `error`). `runAgentConnection` records `agent.connected` on connect and
  `agent.disconnected` {reason, region} on exit, org agents only, via `audit.Record` in a
  detached goroutine with a timeout (never blocks the connection path).
- dash0 event display + `events.json` in 4 locales.

### §4 Private Locations page
- `GET /private-regions` rows gain `onlineAgentCount`, `state`
  (`online|degraded|offline|empty`), `livenessMonitor` {uid, slug, enabled, status} and
  `livenessMonitorOff`. `GET /agents` rows gain `online` (shared rule).
- `POST /private-regions/{slug}/liveness-monitor` re-enables.
- Page: per-agent online/offline `Badge`, per-location state badge + link to the monitor,
  "Liveness monitor off" + re-enable button. Strings in en/fr/de/es (parity test exists).

### §5 Registration checklist
checkerdef (const, metadata, ListCheckTypes, IsPassive/PassiveCheckTypes), registry,
configregistry, schemas regenerated, docs section + anchor, dash0 (`common.ts`,
`api/hooks.ts`, `check-form.tsx` read-only region + no type picker entry for creation,
form module registry, `check-type-identity.tsx`, locales, `check-scheduling.ts` passive
list), OpenAPI check-type enums (all types) + regenerated client, MCP "Allowed:" lists
derived from the metadata table instead of hard-coded.

### Tests
- evaluator table test (Up/Warning/Down/Created, disconnect link), jobs-node evaluation with
  no cloud worker and no agent (PassiveEvaluator + DirectBackend on SQLite), lifecycle
  (create/delete region, backfill exactly one, none after opt-out, enroll re-ensure),
  cross-org rejection, quota exemption, disconnect event with reason, freshness exemption,
  incident opens after confirmation and resolves on reconnect.
