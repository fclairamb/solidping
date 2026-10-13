---
model: opus
effort: high
---

# Run now during an in-flight run never produces a result, and a PATCH can drop an in-flight run's result

## Problem
Two flaky dash0 E2E tests in CI run 38110858942 (PR #508, shard-1 worker on :4010, log `e2e-artifacts-1/solidping-server-w1.log`, 04:22-04:27 UTC) point at the same area: a job row rewritten or left alone while a worker holds its lease.

1. `web/dash0/e2e/check-run-now.spec.ts` "runs an http check and shows the new result". Check with period 1h. Run now clicked 0.8 s after creation, "Run requested." shown, then no "Run finished" for 28 s. The first express run completed at 04:25:48.315, the run-now POST came at 04:25:48.880, and the check never ran again.
   - `RunNow` (`server/internal/handlers/checkrunnow/service.go:305`) returns `runningResponse` without queuing anything when `allRunning` is true (`:329`). When a worker claims the job between the read and the update, `RequestCheckRun` returns `db.ErrCheckJobBusy` (`:352`) and that is swallowed too, and the region is reported `running`.
   - `RequestCheckRun` (`internal/db/postgres/postgres.go:2396`, sqlite `internal/db/sqlite/sqlite.go:2300`) refuses a leased job because the release would overwrite `scheduled_at`. Nothing remembers the request.
   - dash0 waits for a result with `periodStart >= requestedAt`. The in-flight run started earlier, so it never matches. With a 1h period the spinner is infinite.
   - Commit ca5050428 made the test wait for the first result before clicking. That hides the product bug.
2. `web/dash0/e2e/check-dns-change-detection.spec.ts` "captures a baseline, reports a change and accepts it". After PATCHing `config.baseline` to `["10.9.9.9"]` (period 00:00:10), `lastResult.output.changes` stayed null for 60 s. It passed on retry in 13 s.
   - The w1 log in that window has repeated `failed to submit result: failed to release lease: no rows updated: job may have been claimed by another worker`. That comes from `execRelease` (`internal/checkworker/checkjobsvc/service.go:1285`) when `lease_worker_uid` no longer matches. The same message appeared 35 times in a 1-worker run, so it is not shard-specific.
   - Not a baseline lost update: `CaptureCheckConfigBaseline` (`internal/db/postgres/postgres.go` ~2455) only writes an empty key.
   - Suspect: the PATCH reconciles jobs (`internal/handlers/checks/service.go` ~3195-3310) and calls `DeleteCheckJob` then recreates the job (`:3213`, `:3252`, `:3305`). The in-flight worker then fails its release and its result is dropped. The open question is why no later 10 s run reported the change within 60 s.

## Proposal
1. Reproduce both first with Go tests (see Tests). Do not fix on a guess: confirm which of the paths below actually loses the result.
2. Run now during an in-flight run. Record the request instead of dropping it, reusing the pending "Capture now" pattern (`pendingCaptureOr`, `internal/checkworker/checkjobsvc/service.go:1272`, column `capture_requested_at`, `internal/db/models/check_job.go:77`).
   - Add a `run_requested_at` column (migration per `wiki/conventions/migrations.md`, both engines).
   - `RequestCheckRun` on a leased job (and the `ErrCheckJobBusy` race) sets `run_requested_at` instead of refusing.
   - The release paths make the job due at the request time when `run_requested_at` is set, then clear it. Do this in every release variant (`:1175`, `:1219`, `:1251`, `:1316`, `:1349`).
   - `RunNow` reports `queued`, not `running`, for those regions, and still spends no extra budget beyond one `Admit`.
   - Alternative, if the migration is judged too heavy: have dash0 accept the in-flight run's result when the response says `running`. Prefer the server fix, because it also makes Run now honest for API callers.
3. PATCH versus in-flight run. In `internal/handlers/checks/service.go` reconcile, stop deleting and recreating a job that is leased. Update it in place, or defer the replacement until the lease is released. The in-flight worker's result must be saved and its release must not fail.
4. Confirm why no later run reported the new baseline in the DNS test (stale `scheduled_at` after recreate? change detection comparing against a baseline cached at claim time?) and fix that cause too.
5. Keep the E2E tests deterministic. Revert the workaround in `check-run-now.spec.ts` so it clicks Run now right after creation, and keep that as the regression test. Do not add retries or longer timeouts.

## Tests
- `server/internal/handlers/checkrunnow/`: with a job leased by a worker, `RunNow` records the request. After the lease is released, the job is due at `requestedAt` and a second run happens. Negative: no pending request means the release schedules the normal next period. Race: worker claims between the read and `RequestCheckRun` (`ErrCheckJobBusy`), and the request is still honoured.
- `server/internal/db/{sqlite,postgres}`: `RequestCheckRun` on a leased job sets the pending marker. The release clears it and sets `scheduled_at` to the request time.
- `server/internal/handlers/checks/`: PATCH (config change, period change) while a job is leased. The worker's `SubmitResult` and lease release succeed (no `ErrJobClaimedByAnother`), the result is stored, and exactly one job row remains.
- DNS change detection integration test: PATCH `config.baseline` mid-run. The next result within two periods carries `output.changes`.
- E2E, `web/dash0/e2e/check-run-now.spec.ts` and `check-dns-change-detection.spec.ts`: run with `--repeat-each=20` under CPU load (side-car server, `wiki/testing/local-e2e.md`). Zero failures and no `no rows updated` lines in the server log.

## To verify
- Whether the reconcile really deletes and recreates a leased job on PATCH, or only updates schedule fields. This decides step 3.
- Which release path logged `no rows updated` in the CI log: a delete/recreate, a lease expiry, or a second worker claiming a job whose lease had expired mid-run.
- Whether `run_requested_at` can reuse `capture_requested_at` semantics, or whether the two must stay separate (a capture request fires a screenshot run).
- Pre-existing 35 occurrences in the 1-worker run: see if they share the cause or are a separate bug.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**
- Server-side pending request (new column, recommended) or UI-only fix (dash0 accepts the in-flight result)? Recommended: server-side, because API callers get the same wrong "running" answer.
