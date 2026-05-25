# Network Discovery: large ranges via fan-out into bounded child jobs

## Context

The discovery "new" page rejects anything bigger than a /20:

```json
{
  "title": "discovery range too large: 65536 addresses (max 4096, use /20 or smaller)",
  "code": "DISCOVERY_RANGE_TOO_LARGE"
}
```

The 4096 cap (`server/internal/discovery/safety.go:14`, `MaxAddresses`) is a guardrail
bolted on top of a scanner that doesn't scale, rather than a real limit. Three concrete
problems sit underneath it:

1. **The scanner is fully batch, not streaming.** `disc.Scan`
   (`server/internal/discovery/scanner.go:53`) materializes every address into one
   `[]net.IP`, spawns **one goroutine per IP** (the `concurrency` semaphore only
   throttles probing, not goroutine creation), collects results into one slice, and
   returns them all at once. The job runner then persists everything only **after** the
   whole scan finishes (`server/internal/jobs/jobtypes/job_network_discovery.go:65-78`).

2. **Nothing is visible until the very end — the worse UX bug.** Hosts land in the DB
   only at completion, and the scan-detail page doesn't even auto-poll the host list
   (only the status badge polls every 3s; the table refreshes on manual click / dismiss
   — `web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx`). A large scan shows an
   empty table for many minutes, then everything at once.

3. **Background jobs have no lease / heartbeat / timeout / cancel-while-running.**
   `jobsvc` only does `pending → running → success/failed`; `CancelJob`
   (`server/internal/jobs/jobsvc/service.go:357`) works **only on pending** jobs. So a
   monolithic long scan has no stop button, and if the worker restarts mid-scan the job
   is stuck `running` forever — and the per-org `checkAlreadyRunning` guard
   (`server/internal/handlers/discovery/service.go:176`) then **permanently blocks all
   future scans** for that org.

### Design decision

Literally removing the cap (one line) "works" but leaves invisible progress, no
cancellation, and a self-inflicted permanent lockout on crash. Instead we keep a **fixed
per-job cap (~4096)** and make the *overall scan* arbitrarily large by **fanning out**:
the initial request creates a lightweight **plan** job that splits the requested CIDRs
into ≤4096-address chunks and schedules one ordinary `network_discovery` child job per
chunk. This is what makes the scan naturally **interruptible** (cancel = drop the
not-yet-started child jobs), **crash-resilient** (a dead child loses one bounded chunk,
not the whole scan, and the existing retry path covers it), and **progressive** (hosts
appear chunk-by-chunk; progress = chunks done / total). It needs no new lease/heartbeat
machinery because every job stays short.

The existing scanner (`disc.Scan`, `probeHost`, …) is **reused unchanged** — it already
handles ≤4096 fine. The new work is a coordinator layer plus progress/cancel plumbing.

## Goals

1. Accept large ranges (well beyond /20) on the discovery "new" page.
2. Split a scan into bounded child jobs (~4096 addresses each) that run independently.
3. Show progress while running and stream hosts into the table as chunks finish.
4. Let an admin **stop** a running scan (cancels pending chunks).
5. Keep a single fixed overall ceiling so a `/8` can't accidentally create thousands of
   jobs.

## Non-goals

- Rewriting `disc.Scan` into an internal streaming/worker-pool design — chunking at the
  job layer already bounds memory and goroutines. (Possible future optimization; not
  needed now.)
- A generic job lease/heartbeat/reaper subsystem. The fan-out *mitigates* the
  orphaned-`running`-child problem (bounded blast radius); full orphan recovery is noted
  as future hardening, not built here.
- IPv6 (still IPv4-only, unchanged).
- Changing promote / dismiss / suggested-checks behavior.

## Design

### Job topology

- **New job type** `network_discovery_plan` (`jobdef.JobTypeNetworkDiscoveryPlan`).
  `StartScan` creates this instead of a direct `network_discovery` job and returns it;
  **its UID is the scan UID** shown at `/discovery/$jobUid`.
- **Plan runner** (`server/internal/jobs/jobtypes/job_network_discovery_plan.go`, new):
  splits CIDRs into chunks, creates one `network_discovery` **child** job per chunk with
  config `{ cidrs: <chunk>, ports, timeout, concurrency, parentJobUid: <planUID> }`,
  records the total chunk count in its `Output` (progress denominator), then completes
  `success`. Scheduling the child rows is the only thing it does, so it's fast.
- **Child grouping via config, no migration.** Children carry `parentJobUid` in their
  JSON config. `GetScan` finds them with
  `WHERE type='network_discovery' AND config->>'parentJobUid' = <planUID>`. Counts are
  small (≤ the overall ceiling), so no index is needed. A `parent_job_uid` column is the
  cleaner long-term alternative (see Open considerations).
- **Hosts roll up under the parent.** The child runner persists `discovered_hosts` with
  `job_uid = parentJobUid` (not the child's own UID), so the existing
  list-hosts-by-jobUid query and the whole frontend work unchanged. Add
  `ParentJobUID string \`json:"parentJobUid,omitempty"\`` to `disc.Config`
  (`scanner.go:32`); the synchronous `Scan` ignores it, and the child runner uses it at
  the `NewDiscoveredHost(orgUID, jobUID, …)` call (`job_network_discovery.go:103`).

### CIDR splitting + ceiling

- New helper in the discovery package, e.g.
  `SplitCIDRs(cidrs []string, maxPerChunk int) ([][]string, error)`: subdivides each CIDR
  into ≤ /20 blocks and packs them into chunks of ≤ `maxPerChunk` addresses. Reuse
  `cidrSize` (`safety.go:63`).
- Keep `MaxAddresses = 4096` as the **per-chunk** cap (`ValidateCIDRs` and the child
  `CreateJobRun` path stay exactly as-is).
- Add a **fixed overall ceiling** — `MaxScanChunks` (e.g. 256 → ≈1M addresses, a /12) —
  enforced by a new `ValidatePlanCIDRs`. Exceeding it returns the existing
  `DISCOVERY_RANGE_TOO_LARGE` code with an updated message. This honors the
  "higher fixed cap" decision while delivering effectively-unlimited ranges via chunking.

### "Already running" guard (derived, not plan-status)

The plan job goes `success` as soon as it finishes scheduling, so the guard can't key on
the plan's own status. Redefine an active scan as: **a plan job pending/running, OR any
child (`network_discovery`) pending/running for the org**. To keep a single stuck
`running` child from blocking forever, ignore children whose `updated_at` is older than a
`staleScanThreshold` (e.g. 30m) when evaluating the guard. This is the pragmatic
stand-in for a real reaper.

### Cancel

- New endpoint `POST /orgs/:org/discovery/scans/:jobUid/cancel` (admin only).
- Service `CancelScan(orgUID, planUID)`: cancel the plan job if still pending, then
  `CancelJob` each **pending** child (soft-delete — `CancelJob` already restricts to
  pending). Running children (bounded, ~minutes) finish naturally. Net effect: no new
  chunks start.

### Progress (`GetScan` aggregation)

`GetScan` returns the plan job plus a derived block:
`{ totalChunks, completedChunks, failedChunks, runningChunks, pendingChunks,
derivedStatus, hostCount }`. `derivedStatus` = `running` while the plan is running or any
child is pending/running; `success` once all children are terminal; `failed` only if the
plan itself failed. This drives the progress UI.

### Frontend

- `discovery.$jobUid.index.tsx`: add `refetchInterval` to the **host list** query (poll,
  e.g. 3s, while `derivedStatus` is pending/running) so hosts stream into the table; show
  a progress indicator (`completedChunks / totalChunks`, addresses scanned) and a **Stop
  scan** button wired to the cancel endpoint (use design-reference primitives per
  `web/dash0/CLAUDE.md`).
- `discovery.new.tsx`: keep the existing confirmation checkbox as the large-range
  guardrail; update helper text to reflect the new ceiling (no longer "/20 or smaller").
- Regenerate the API client/hooks for the new cancel endpoint + `GetScan` progress fields
  (`make generate`).

## Files to touch (representative)

- `server/internal/discovery/safety.go` — `MaxScanChunks`, `ValidatePlanCIDRs`,
  `SplitCIDRs`.
- `server/internal/discovery/scanner.go` — add `ParentJobUID` to `Config`.
- `server/internal/jobs/jobdef/types.go` — `JobTypeNetworkDiscoveryPlan`.
- `server/internal/jobs/jobtypes/job_network_discovery_plan.go` — **new** plan runner.
- `server/internal/jobs/jobtypes/registry.go` — register the plan job.
- `server/internal/jobs/jobtypes/job_network_discovery.go` — persist hosts under
  `parentJobUid`.
- `server/internal/handlers/discovery/service.go` — `StartScan` creates a plan; derived
  `checkAlreadyRunning`; `CancelScan`; `GetScanProgress` aggregation.
- `server/internal/handlers/discovery/handler.go` — cancel route + handler; enrich
  `GetScan`.
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx`, `discovery.new.tsx`,
  `web/dash0/src/api/hooks.ts` (regenerated).
- Tests: split/ceiling unit tests; plan-runner fan-out test; cancel + guard service
  tests; a dash0 Playwright e2e for a multi-chunk scan showing progress + stop.

## Open considerations

- **Runner parallelism**: chunks process in parallel only if the worker runs several
  concurrent job-runner loops; otherwise they serialize. Verify/raise runner concurrency
  so large scans aren't fully sequential.
- **Orphaned `running` child** still blocks the guard until `staleScanThreshold`; a real
  reaper / lease is future hardening.
- **`parent_job_uid` column** vs config key — config chosen to avoid a migration.

## Verification

1. `make dev` (or apply to the running :4000 server); log in to `org2`.
2. Start a scan on a range > /20 (e.g. a /18 = 4 chunks) via the "new" page → expect 201
   with a plan job, no `DISCOVERY_RANGE_TOO_LARGE`.
3. Watch the scan-detail page: progress climbs `1/4 → 4/4` and hosts appear chunk-by-chunk
   (not all at the end).
4. Start a bigger scan and click **Stop** mid-run → pending chunks vanish, scan settles,
   and a fresh scan can be started (guard cleared).
5. Confirm a range exceeding `MaxScanChunks` is still rejected with
   `DISCOVERY_RANGE_TOO_LARGE` (updated message).
6. `make test` (split/ceiling/fan-out/cancel) + `make test-dash` (progress + stop e2e).

## Implementation Plan

1. **discovery/safety.go** — Add `MaxScanChunks = 256` (per-chunk cap stays
   `MaxAddresses = 4096`). Add `SplitCIDRs(cidrs, maxPerChunk) ([][]string, error)`:
   subdivide each CIDR into ≤ /20 blocks, pack into chunks of ≤ `maxPerChunk` addresses,
   return chunk slices. Add `ValidatePlanCIDRs(cidrs)` that validates each CIDR, computes
   total chunk count, and rejects (`ErrRangeTooLarge`, updated message) when
   `chunks > MaxScanChunks`. Reuse `cidrSize`.
2. **discovery/scanner.go** — Add `ParentJobUID string \`json:"parentJobUid,omitempty"\``
   to `Config`. `Scan` ignores it.
3. **jobdef/types.go** — Add `JobTypeNetworkDiscoveryPlan = "network_discovery_plan"`.
4. **jobtypes/job_network_discovery_plan.go** (new) — Plan runner: `SplitCIDRs` the config
   CIDRs, create one `network_discovery` child job per chunk via `jobSvc.CreateJob` with
   `parentJobUid = planUID`, record `{ totalChunks }` in plan `Output`, complete success.
   Uses `jctx.Services.Jobs` (jobsvc) for child creation.
5. **jobtypes/registry.go** — Register `NetworkDiscoveryPlanJobDefinition`.
6. **jobtypes/job_network_discovery.go** — Child runner persists hosts under
   `parentJobUid` when present (else its own UID).
7. **handlers/discovery/service.go** — `StartScan` validates via `ValidatePlanCIDRs`,
   creates a `network_discovery_plan` job (its UID is the scan UID); derived
   `checkAlreadyRunning` (plan or child pending/running, ignoring stale children older than
   `staleScanThreshold = 30m`); `CancelScan(orgUID, planUID)`; `GetScanProgress` aggregation
   returning `{ totalChunks, completedChunks, failedChunks, runningChunks, pendingChunks,
   derivedStatus, hostCount }`. Dialect-aware `config->>'parentJobUid'` / `json_extract`.
8. **handlers/discovery/handler.go** — `POST /scans/:jobUid/cancel` (admin); enrich `GetScan`
   with the progress block.
9. **Frontend** — `hooks.ts`: add `DiscoveryScanProgress` to `DiscoveryScan`, `useCancelScan`,
   poll the host-list query while running. `discovery.$jobUid.index.tsx`: progress indicator +
   Stop button. `discovery.new.tsx`: update helper text (drop "/20 or smaller").
10. **Tests** — split/ceiling unit tests (safety_test.go); plan-runner fan-out test;
    cancel + derived-guard service tests; Playwright e2e for multi-chunk progress + stop.
