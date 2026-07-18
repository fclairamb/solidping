---
model: sonnet
effort: medium
---

# `workers.Service.ClaimJobs` has zero production callers and should be removed

## Problem

[`server/internal/handlers/workers/service.go:89`](server/internal/handlers/workers/service.go:89)
defines `(*Service).ClaimJobs`, plus its `ClaimJobsRequest` /
`ClaimJobsResponse` types (`service.go:71` and `service.go:78`). It became
dead code when spec `2026-07-16-02` removed the HTTP edge-worker API (commit
"feat: remove the HTTP edge-worker API, spw_ tokens, and the workers.token
column"): the deported-agent WebSocket path calls
`checkjobsvc.Service.ClaimJobsForAgent` directly
([`server/internal/handlers/agentws/handler.go:481`](server/internal/handlers/agentws/handler.go:481)),
and the in-process path goes through
`checkworker/backend.DirectBackend.ClaimJobs`
([`server/internal/checkworker/backend/direct.go:82`](server/internal/checkworker/backend/direct.go:82)).
Neither reaches `workers.Service.ClaimJobs`.

This was noticed in passing while implementing spec `2026-07-16-05`
(config_private decrypt on the in-process claim path), which refactored
`workers.Service.ClaimJobs` onto the shared `checkjobsvc.MergeJobSecrets`
helper rather than deleting it, since removal was out of scope there.

Independent verification already done for this spec (re-verify before
deleting — don't just trust this):

- `grep -rn '\.ClaimJobs(' --include="*.go" server/` (excluding tests) hits
  exactly three receivers: `checkworker/worker.go:481` (→ `backend.ClaimJobs`,
  the `DirectBackend`/`WSBackend` interface), `checkworker/backend/direct.go:82`
  (→ `checkJobSvc.ClaimJobs`, a different, still-used `checkjobsvc.Service`
  method), and `handlers/workers/service.go:97` itself, calling
  `checkJobSvc.ClaimJobs` from inside the dead method. None of these is a call
  *into* `workers.Service.ClaimJobs`.
- `grep -rn "ClaimJobsRequest\|ClaimJobsResponse"` finds these types used
  nowhere outside `service.go` — no caller ever constructs a request.
- The only `workers.Service` instance in production is `agentWorkersSvc`,
  built at [`server/internal/app/server.go:805`](server/internal/app/server.go:805)
  and passed into `agentws.NewHandler`. That handler only calls
  `h.workersSvc.SubmitResult` ([`agentws/handler.go:547`](server/internal/handlers/agentws/handler.go:547));
  it never calls `.ClaimJobs` on it.
- `agentws/handler_test.go` constructs a `workers.Service` too (for
  `SubmitResult` coverage) but never calls `.ClaimJobs` on it either. The
  `wsBackend.ClaimJobs` calls in that file (lines 592, 644) hit
  `backend.WSBackend.ClaimJobs`, an unrelated type.
- No route in `server/internal/app/server.go` reaches
  `workers.Service.ClaimJobs` — the HTTP edge-worker routes were removed
  entirely by spec `2026-07-16-02`.

**Re-run this verification fresh before deleting anything** — the codebase
may have moved since this spec was filed.

## Proposal

If verification confirms the method is still dead:

1. Delete `(*Service).ClaimJobs`, `ClaimJobsRequest`, and `ClaimJobsResponse`
   from [`server/internal/handlers/workers/service.go`](server/internal/handlers/workers/service.go).
   Leave `Heartbeat` and `SubmitResult` (and their request/response types)
   alone — both are still live.
2. Check whether the `creds credentials.Service` field on `Service` (used
   only inside `ClaimJobs`, via `checkjobsvc.MergeJobSecrets`) becomes unused
   once `ClaimJobs` is gone. If so, drop the field and the `creds` parameter
   from `NewService`, and update both call sites:
   [`server/internal/app/server.go:805`](server/internal/app/server.go:805)
   (`workers.NewService(s.dbService, s.services.CheckJobs, incidents.NewService(...), s.services.Credentials)`)
   and
   [`server/internal/handlers/agentws/handler_test.go:63`](server/internal/handlers/agentws/handler_test.go:63).
   Do **not** touch `checkjobsvc.MergeJobSecrets` itself — `DirectBackend.ClaimJobs`
   is still a live caller of that helper.
3. Update the package doc comment at the top of `service.go` (currently says
   "What remains is the transport-agnostic service logic — ClaimJobs (with
   server-side secret handling) and SubmitResult...") to stop describing
   `ClaimJobs` as something the package still provides.
4. Remove any test code that exists solely to exercise
   `workers.Service.ClaimJobs` (search test files for `ClaimJobsRequest`,
   `ClaimJobsResponse`, or `workersSvc.ClaimJobs` / `agentWorkersSvc.ClaimJobs`
   — as of filing, none were found, but re-check).
5. Leave `ErrWorkerNotFound` alone even if it looks unused — it's pre-existing
   and unrelated to this cleanup; don't scope-creep into it.

If step 1's fresh verification instead turns up a real caller, stop and
report where — do not delete anything.

**Gates:** `make build-backend lint-back test` must pass. `make lint-back` is
currently green (0 issues) — keep it that way; never relax `.golangci.yml` to
get there.

**Branching:** work on a fresh branch off `main`. The working tree may be
checked out on a shared batch branch (e.g. `batch/2026-07-16`) used by
concurrent automations — do not commit there or change its checked-out
branch; create/use a separate branch or worktree instead.

## Implementation Plan

1. **Re-verify dead code fresh.** Ran `grep -rn '\.ClaimJobs(' --include="*.go"
   server/` (excluding tests) — 3 hits, none a call into
   `workers.Service.ClaimJobs`: `checkworker/worker.go:481` (→
   `backend.ClaimJobs`), `checkworker/backend/direct.go:82` (→
   `checkJobSvc.ClaimJobs`), and `handlers/workers/service.go:97` itself
   (calling `checkJobSvc.ClaimJobs` from inside the dead method). Also ran
   `grep -rn "ClaimJobsRequest\|ClaimJobsResponse"` — only hits inside
   `service.go` itself (declaration + usage). Grepped all `*_test.go` for
   `.ClaimJobs(` — every hit resolves to `checkjobsvc.Service.ClaimJobs` or
   `backend.{Direct,WS}Backend.ClaimJobs`, never `workers.Service.ClaimJobs`.
   Confirmed dead — proceeding with deletion.
2. **Delete dead code in `server/internal/handlers/workers/service.go`:**
   remove `ClaimJobsRequest`, `ClaimJobsResponse`, and `(*Service).ClaimJobs`
   (lines ~70-125). Leave `Heartbeat`, `SubmitResult`,
   `SubmitResultRequest/Response`, `ErrWorkerNotFound` untouched.
3. **Drop the now-unused `creds` field.** `creds credentials.Service` on
   `Service` was only read inside `ClaimJobs` (via
   `checkjobsvc.MergeJobSecrets`) — confirmed no other reference in
   `service.go`. Remove the field, its doc comment, and the `creds` param
   from `NewService`. Update call sites:
   - `server/internal/app/server.go:805` — drop the trailing
     `s.services.Credentials` arg from `workers.NewService(...)`.
     `s.services.Credentials` itself stays (used at many other call sites in
     that file) — only this one argument goes away.
   - `server/internal/handlers/agentws/handler_test.go:63` — drop the
     trailing `creds` arg. `creds, err := credentials.NewService(nil,
     credentials.ParamStore{})` (lines 60-61) becomes dead too (confirmed
     `creds` used nowhere else in the file) — remove those two lines. The
     `credentials` package import stays: still used for
     `credentials.SealForRecipients` / `credentials.UnsealWithIdentity`
     elsewhere in the same file.
4. **Update the package doc comment** at the top of `service.go` (currently
   "What remains is the transport-agnostic service logic — ClaimJobs (with
   server-side secret handling) and SubmitResult...") to describe only
   `SubmitResult` (plus `Heartbeat`).
5. **No test deletions needed** — confirmed via the test grep in step 1 that
   no test exists solely to exercise `workers.Service.ClaimJobs`;
   `agentws/handler_test.go`'s `wsBackend.ClaimJobs` calls hit an unrelated
   type and stay as-is (only the `creds` construction feeding
   `workers.NewService` is removed, per step 3).
6. **`make fmt`**, then `make build-backend lint-back test`. Fix and commit
   any failures (explicit paths, no `.golangci.yml` changes).
7. Commit sequence: deletion + doc comment update as one commit, `creds`
   field/param cleanup + call-site updates as a second commit (logically
   separate cleanups), then a final `chore: all checks passing...` commit
   once gates are green.
