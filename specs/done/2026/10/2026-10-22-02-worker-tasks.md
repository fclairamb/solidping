---
model: opus
effort: high
---

# Work that runs on a worker in several steps has no shared mechanism, and never reaches an agent

Part of the agent-run work series: 2026-10-21-01 (region on discovered checks), 2026-10-21-02
(scanner ICMP), **this spec** (worker tasks), 2026-10-22-03 (crawl on worker tasks, from private
locations), 2026-10-22-04 (discovery from an agent), 2026-10-22-05 (dashboard and docs). Implement
in that order. This spec ships the mechanism and no production kind: specs 03 and 04 add `crawl`
and `discovery`.

## Problem
Two features need the same thing: a unit of work that a worker claims, holds under a lease, may
run in several slices with state in between, and reports back.

- The website crawl (spec 2026-10-03-03, shipped in v0.38.0) does it inside `check_jobs`:
  - columns `step_run_uid`, `step_run_started_at`, `step_count`, `step_state_file_uid` and
    `step_failures` (`server/internal/db/models/check_job.go:110-114`);
  - the bulk lane claim (`checkworker/checkjobsvc/service.go:1016-1057`);
  - `checkworker/worker_steps.go` and the in-process `StepBackend`
    (`checkworker/backend/backend.go:196-216`).

  Only `DirectBackend` implements it, so agents never claim a crawl
  (`checkjobsvc/service.go:689-691`). Validation refuses a crawl on a private location
  (`handlers/checks/validate.go:36-37, 466-478`).
- Agent discovery (spec 2026-10-22-04) needs the same lease, fencing, attempts and HTTP transport
  for chunks claimed by an agent of an exact `(org, region)`.

Writing the mechanism twice would give two lease implementations, two fencing rules and two agent
protocols.

## Decisions
Taken 2026-10-22:
1. One table, `worker_tasks`, for every kind. The crawl moves onto it (spec 03) and the
   discovery chunks use it (spec 04). There is no `discovery_chunks` table.
2. The worker pulls; the server never pushes. Agents pull over signed HTTP, never over the
   WebSocket. Reasons, from the 2026-10-21 review:
   - the agent socket registry is per replica (`handlers/agentws/uploads.go:20-26, 40-43`);
   - a WebSocket frame over 256 KiB closes the agent connection (`handlers/agentws/handler.go:65-69`);
   - an unknown client frame type closes it too (`handler.go:994`);
   - an old server answers 404 to an HTTP route, which the agent treats as "nothing to do".
3. Cloud workers use the same service in-process, through the same interface as agents. There
   is one claim path and one set of fencing rules.
4. The server owns the state. A worker receives the state in the claim and sends the next state
   back with the step. Nobody but the server writes the state attachment.
5. A worker never authors anything the server stores as-is. Each kind validates what comes back
   (spec 04 recomputes discovery suggestions, for example).

## Proposal
1. **Migration**, appended to the untagged `030_v0_41_0.tx.up.sql` / `.tx.down.sql` (both
   engines; re-check with `git tag --contains`, and add the next `NNN` if a tag contains it by
   then). Table `worker_tasks`:
   - `uid`; `organization_uid` (fk, cascade); `kind text not null`; `region text not null`;
   - the parent: `check_uid` (nullable fk `checks(uid)` on delete cascade) and `job_uid`
     (nullable fk `jobs(uid)` on delete cascade, so it is cleaned with the plan by
     `job_jobs_cleanup.go`). A check constraint requires exactly one of the two;
   - `payload` (JSON text, the kind's input); `progress` (JSON text, small, shown in the UI);
   - `status` (`pending|claimed|done|failed|cancelled`, checked);
   - lease: `worker_uid` (nullable), `lease_until` (nullable), `attempts int not null default 0`
     (claims of the current slice);
   - steps: `slice_count int not null default 0`, `slice_failures int not null default 0`,
     `state_file_uid` (nullable), `not_before` (timestamp, not null), `run_started_at`;
   - `error_code` (nullable); `created_at`; `updated_at`.

   Indexes:
   - the claim index `(region, kind, status, not_before)`;
   - `(organization_uid, region, status)`;
   - `(job_uid)`;
   - a unique partial index on `(check_uid) WHERE status IN ('pending','claimed')`, so a check
     has at most one active run.

   Add the model, then the db methods on both engines.
2. **Service** `server/internal/workertasks/`:
   - `Claim(ctx, WorkerIdentity{UID, Region, OrgUID, IsAgent, Kinds, Slots})` returns one task or
     none. In one atomic statement it takes the oldest row where all of these hold:
     - the kind is in `Kinds`;
     - the region equals the worker's region exactly;
     - the org is the agent's org (agents only; a cloud worker serves every org of its region);
     - `not_before <= now`, and the status is `pending`, or `claimed` with `lease_until < now`;
     - the worker holds fewer live leases than `Slots`.

     It sets `claimed`, `worker_uid`, `lease_until = now + 2 min` and `attempts + 1`. A row that
     would reach a 4th attempt becomes `failed` / `max_attempts` instead (the kind's `Fail` hook
     runs), and the next row is tried. Postgres uses `FOR UPDATE SKIP LOCKED`; SQLite uses
     `UPDATE … WHERE uid = (SELECT … LIMIT 1) RETURNING`.
   - `Heartbeat(uid, workerUID)` extends the lease by 2 min. It returns `ErrTaskGone` when the
     task is cancelled or not held by this worker.
   - `Step(uid, workerUID, {state, progress, units})` handles a non-final slice:
     - it stores the state as an attachment (1 MiB cap, keep 2, reusing the step-state topic
       helpers of `handlers/attachments/topics.go:33-104`);
     - it moves `state_file_uid`, bumps `slice_count`, resets `slice_failures`, sets `pending`
       and `not_before = now`, and clears the lease.
   - `Finish(uid, workerUID, body)` hands the body to the kind's `Complete` hook, then sets `done`
     or `failed` with the `error_code` the hook returns.
   - `FailSlice(uid, workerUID, errorCode)` bumps `slice_failures`. Three in a row → `failed`
     and the `Fail` hook; otherwise `pending` with `not_before = now + 1 min`.
   - `Cancel(filter)` sets the `pending` and `claimed` rows of a check or a job to `cancelled`.
   - Fencing: every write except `Claim` matches `worker_uid`, `status = 'claimed'` and a live
     lease. A worker whose lease expired changes nothing (`ErrTaskGone`).
   - Lazy expiry, no sweeper: readers count a `pending` row older than 24 h as failed.
3. **Kinds**, registered at startup on both sides:
   - **Server side:** `TaskKind{Name, PrepareClaim, Complete, Fail}`.
     - `PrepareClaim` adds what the worker needs to the claim: the decoded state, the slice
       budget, the units, `final`, the kind's input. It may also defer the task: it returns
       `deferUntil`, the claim is released without counting an attempt, and `not_before` moves.
     - `Complete` validates and persists the result.
     - `Fail` records a terminal failure (spec 03: an error result on the check).
   - **Worker side:** `TaskExecutor{Kind, Capability, Run(ctx, claim, reporter)}`. The reporter
     exposes heartbeat, step, finish and fail.
   - Tests register a fake kind. No production kind ships here.
4. **Transport.**
   - Cloud workers use an in-process client (next to `DirectBackend`).
   - Agents use an HTTP client on these routes, registered next to `/agent/attachments`
     (`app/server.go:1479`) and authenticated with its Ed25519 signed headers:
     - `POST /api/v1/agent/tasks/claim` (`{kinds}`) → `200` or `204`;
     - `POST /api/v1/agent/tasks/:uid/heartbeat` → `200` or `410`;
     - `POST /api/v1/agent/tasks/:uid/step` → `200`, `410` or `413`;
     - `POST /api/v1/agent/tasks/:uid/finish` → `200`, `410` or `413`;
     - `POST /api/v1/agent/tasks/:uid/fail` → `200` or `410`.
   - Extract `attachments.Handler.authenticate` (`handlers/attachments/handler.go:248`) into a
     shared helper; do not copy it.
   - A revoked agent gets 401. A system agent gets 403: it serves cloud regions, which cloud
     workers already cover.
   - Bodies are capped at 1 MiB (`413` above). The claim body's `kinds` is intersected with the
     server's registry. An agent holds 1 slot.
5. **Worker loop** `server/internal/checkworker/tasks/`:
   - Each registered executor gets a claim loop. On `204`, `404` (old server) or an error, it
     sleeps 15 s with jitter. Heartbeats run every 30 s while `Run` works; `ErrTaskGone`
     (`410`) cancels `Run`'s context.
   - Cloud workers run it with `Slots = scheduling.bulk_lane_max` (default 4,
     `checkworker/scheduling/scheduling.go`), sharing the worker pool as the bulk lane does today.
   - Agents start it from `agentmode/agentmode.go:94-114`.
   - It runs under the worker's egress guard: `egress.WithGuard(ctx, guard)`, with the guard of
     `WSBackend` on an agent (`checkworker/backend/ws.go:90-92`).
   - A panic in `Run` is recovered and reported as `FailSlice`.
6. **Capabilities.** An agent reports one capability per executor it runs: the executor's
   `Capability`, added in `backend.Capabilities()` (`checkworker/backend/capabilities.go:18-41`)
   only when the egress set is non-nil (appending to a nil set turns "unknown" into "no
   IPv4/IPv6"). Add the names to `aggregatedCapabilities` (`regions/capabilities.go:91`). A
   system agent's report never counts. Specs 03 and 04 add `crawl` and `discovery`.
7. **Logs.** On agents: INFO per claimed task (uid, kind, slice, duration), WARN per refusal or
   failure. The agent has no database, so this log is its only record.

## Tests
- `workertasks` service, both engines, with a fake kind:
  - **Claim scoping:** an agent claim returns only its org and exact region. Two orgs that both
    define `@dc1` never claim each other's tasks. A cloud worker of `eu-west` claims tasks of
    any org in `eu-west` and none of `@dc1`.
  - **Slots and leases:** a second claim while the only slot is held → none. An expired lease is
    reclaimable, attempts increment, and the 4th attempt becomes `failed` / `max_attempts` with
    `Fail` called once.
  - **Steps:** `Step` stores the state and the next claim returns it. A state over 1 MiB → error,
    state unchanged. Three `FailSlice` in a row → `failed`.
  - **Fencing:** `Heartbeat`, `Step` and `Finish` from another worker, after expiry, or on a
    cancelled task → `ErrTaskGone`, nothing written.
  - **Deferral:** `PrepareClaim` returning `deferUntil` releases the claim without counting an
    attempt.
  - **One active run per check:** a second active task for the same check is refused by the
    unique index.
- HTTP routes:
  - signed auth, revoked → 401, system agent → 403;
  - a body over 1 MiB → 413;
  - unknown kinds are ignored;
  - an old-server 404 makes the agent loop sleep, not crash.
- Worker loop: a `410` on heartbeat cancels `Run`; a panic becomes `FailSlice`; the egress guard
  is on the context `Run` sees.
- `regions`: a capability is aggregated from org agents only, never from a system agent.
- `make test-postgres` for the claim SQL.

## To verify
- The db layer shape for a new table (interface in `db/service.go`, sqlite and postgres
  implementations), and whether bulk insert exists.
- Whether the capability aggregation (`regions/capabilities.go:187-217`) can see `IsSystem()`,
  or needs a join.
- Whether `/agent/attachments` is in `openapi.yaml`. Document the task routes the same way, or
  leave them out the same way.
