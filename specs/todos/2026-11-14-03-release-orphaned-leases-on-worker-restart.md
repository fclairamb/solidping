---
model: opus     # lease ownership under concurrency: a wrong release double-runs checks or drops results
effort: high
pr: batch
automerge: false
---

# A restarted worker leaves its leased check jobs locked for a full period

## Problem

When a worker crashes or its node reboots, the jobs it had leased stay locked until
`lease_expires_at`. The lease is set to `max(scheduled_at, now) + period + 30s`
(`internal/checkworker/checkjobsvc/service.go:1005`, `updateSingleJobLease`). The restarted process
comes back under the **same worker UID**: `RegisterOrUpdateWorker` matches by slug
(`internal/db/postgres/postgres.go:1572`, `internal/db/sqlite/sqlite.go:1561`), and an agent's row is
keyed by `agentcrypto.WorkerSlug(agent.UID)` (`internal/handlers/agentws/handler.go:499`). But the new
process doesn't know it holds those leases. The claim query only takes jobs with
`lease_expires_at IS NULL OR lease_expires_at < now` (`service.go:583`, `:648`, `:743`, `:845`, `:933`),
so nobody runs them until the lease expires.

Production, 2026-11-14: the weekly k8xp `node-upgrade` rebooted every node around 21:01–21:05 UTC.
The kansas-city agent (`ag-4231fca60378`) had leased a 30-minute HTTP check at 21:00:59, 18 s before
us1 rebooted. The lease ran until 21:31:59. The lauterbourg worker had the same problem: leased at
21:03:03, expiring at 21:34. Those checks got no results for about 31 minutes, and the watchdog raised
`[WARNING] dark-region:kansas-city` ("5 overdue, oldest overdue by 20m32s") while the region had a
live worker. A 24 h check would miss a whole day. This happens on every node upgrade.

## Proposal

1. Add `ReleaseWorkerLeases(ctx, workerUID string) (int, error)` to the `checkjobsvc.Service`
   interface (`service.go:32`), with an implementation next to `DeferLeaseRateLimited`
   (`service.go:1137`). It does one `UPDATE check_jobs` on `WHERE lease_worker_uid = ?` and sets:
   - `lease_worker_uid = NULL`, `lease_expires_at = NULL`, `lease_starts = 0`;
   - `scheduled_at` and `effective_scheduled_at` left as they are, so the job is due right away
     (it is already overdue);
   - `capture_requested_at = COALESCE(capture_requested_at, capture_claimed_at)` and
     `capture_claimed_at = NULL`, so a pending "Capture now" request isn't lost (same as
     `DeferLeaseRateLimited`);
   - `run_requested_at` left as it is (no probe ran, so the request is still pending);
   - `step_run_*` left as they are (to verify, see below);
   - `updated_at = now`.

   It returns the number of rows changed. Same SQL for SQLite and Postgres.
2. Direct-DB / in-process worker: call it in `CheckWorker.registerWorker`
   (`internal/checkworker/worker.go:570`) right after `backend.Register` succeeds and before the
   fetcher loop starts. Log at INFO when the count is above 0 (`worker`, `released`). For the
   remote backend (`internal/checkworker/backend/ws.go:223`), do the release on the server side
   when that worker registers, not in the client.
3. Agents (`internal/handlers/agentws/handler.go`): release when the agent connects from a **new
   process**, not on every reconnect (see Open questions). Do it after `ensureWorkerRow`, on the
   enroll path (`:351`) and the reconnect path (`:704`), when the condition below holds.
4. Mention it in `wiki/runbooks/operations-gotchas.md`: a worker restart now frees its leases
   right away, and the dark-region alerts after a node upgrade should stop.

## Tests

- `internal/checkworker/checkjobsvc`, SQLite and Postgres (table-driven, existing DB test
  harness):
  - Jobs leased by worker A with leases still valid → `ReleaseWorkerLeases(A)` returns their count.
    They are claimable right away, `lease_starts = 0`, and `scheduled_at` hasn't changed.
  - Positive control: a job leased by worker B in the same table is untouched (lease, expiry and
    `lease_starts` unchanged).
  - A job with `capture_claimed_at` set comes back with `capture_requested_at` set and
    `capture_claimed_at = NULL`. A job with `run_requested_at` set keeps it.
  - No leases held → returns 0, no error.
- `internal/checkworker/worker_test.go`: a worker that registers with a slug whose UID holds a
  live lease claims that job on its first fetch, without waiting for the lease to expire.
- `internal/handlers/agentws`: an agent connecting from a new process frees its leases. A
  reconnect from the same process (network blip, `ensureWorkerRow` called again) leaves its
  in-flight leases alone, and the result it then submits is still accepted.

## To verify

- What a released job does with `step_run_uid` / `step_run_started_at` / `step_state_file_uid`
  (multi-step checks): reset them, or let the next claim resume. Follow what the stuck-job
  reaper does.
- Whether two direct-DB pods can share a slug, and so a UID (rolling update with `maxSurge: 1`,
  same `SP_NODE_NAME`). If they can, the new pod would free the old pod's in-flight leases. Its
  results would then be refused by the lease-ownership guard, or the check would run twice.
  If it can happen, release only leases claimed before this process started
  (`updated_at < registration time` is not enough on its own; check the claim timestamps).
- Whether the remote-worker path (`backend/ws.go`) registers through a server handler that can
  call `ReleaseWorkerLeases`, or goes through agentws.

## Open questions

- **How does the server tell an agent restart from a network reconnect?** Today enroll and
  reconnect both call `ensureWorkerRow` and carry no per-process identity. Releasing on every
  reconnect would free leases the agent is still running after a short network drop: the check
  runs twice and the agent's late result is refused.
  *Recommended:* the agent generates a random boot ID at start and sends it on enroll and on
  reconnect. The server keeps the last one per worker (new nullable column, new migration) and
  releases only when it changes. An agent too old to send one: no release (today's behaviour).

## Resolved open questions

- **Agent restart vs network reconnect:** the agent generates a random boot ID at start and sends it
  on enroll and on reconnect. The server stores the last one per worker (new nullable column, new
  migration) and releases the worker's leases only when the boot ID changes. An agent too old to send
  one gets no release (today's behaviour). (autopilot: recommended answer)
