---
model: sonnet
effort: medium
---

# Seven tests fail in a full `make test` and pass alone: root-cause the load sensitivity

## Problem
A full `make test` on the maintainer's Mac on 2026-10-17 failed 7 tests in 3 packages. Each package passes alone with `-count=1`, so the failures depend on load or shared resources. `wiki/testing/flaky-tests.md` applies: root-cause, never re-run to green, never loosen an assertion.

Failing tests:
- `server/internal/checkers/checkbrowser`:
  - `TestOpenAgainstARealBrowserMarksAvailable` (`availability_test.go:290`, 30 s open budget)
  - `TestBrowserCheckCapturesARealScreenshot` (`session_live_test.go:343`)
  - `TestBrowserCheckKeepsScreenshotOnRealTimeout` (`session_live_test.go:407`, 8 s probe timeout, already raised once from 3 s for the same symptom: "failed to set the browser viewport: context deadline exceeded")
- `server/internal/checkers/checkerdef`: `TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt`, both subtests (`stranded_connection_test.go:153`). "context deadline exceeded" on the 127.0.0.1 client. Each probe uses a 400 ms timeout (line ~175) and the second probe must dial fresh and answer inside it.
- `server/internal/handlers/auth`: `TestLoginWithOrgSessionCapNeverExceedsIt` (`session_max_duration_test.go:98`). Candidate: `WithinDuration(..., 5*time.Second)` between `loginTime` and the minted `expires_at`, plus the login's password hashing running under CPU contention.

First guess for the browser tests: several tests launch a real Chrome at once, and the packages run in parallel (`-p` default = cores), so Chrome startup is starved. The cause is not confirmed.

## Proposal
1. Reproduce under load without touching the code: run `cd server && go test -short -count=1 -p 16 ./...` (or `make test`) while also saturating CPU. Record for each test the exact `file:line` and message. Use the widen-the-window technique from the wiki (a `time.Sleep` that reproduces the same failure, then the fix must pass with the sleep kept).
2. `checkbrowser`: find what the three tests contend for (Chrome process count, a shared user-data dir, a fixed debugging port, process-wide `withSettings` state, the `liveFixtureServer`). Fix the contention (for example serialise Chrome launches through one package-level semaphore, or give each launch its own profile dir and port). Only if a deadline is shorter than the worst legitimate Chrome cold start, size it from the setup cost, as the existing comment at `session_live_test.go:~420` does. State the measured cold start in the comment.
3. `checkerdef/stranded_connection_test.go`: the 400 ms probe timeout is the stranded-connection trigger and must stay short for the first probe. Find what makes the second probe miss it under load (fresh dial + TLS handshake on a starved scheduler). Give the second, must-succeed probe its own generous deadline, separate from the first probe's deliberately short one. Keep every assertion.
4. `handlers/auth/session_max_duration_test.go:98`: determine which step blew up (bcrypt cost in `Login`, DB setup, or the 5 s `WithinDuration`). Compute the expected expiry from the same clock reading the service used, or inject the clock, instead of widening the tolerance blindly.
5. Add the evidence the wiki asks for if the cause cannot be proven offline (log the Chrome stderr tail on open failure).
6. Open a PR with the root cause of each test stated in the description.

### Linter version (separate, small)
The local `golangci-lint` is v2.14.0; CI pins v2.15.0 (`.github/workflows/ci.yml:207`). The local run reported 7 `nolintlint` "unused directive" findings and 3 `paralleltest` findings in `t.Chdir` tests.
1. Install v2.15.0 locally (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`, call `~/go/bin/golangci-lint` directly) and run `cd server && golangci-lint run ./...`.
2. If the findings disappear, nothing to change in the repo. Report that the local install was stale and mention the version check in `wiki/runbooks/ci-and-release-triage.md` if it is not there already.
3. If they remain, they are real: remove the 7 unused `//nolint` directives, and fix the 3 `t.Chdir` tests (`t.Chdir` forbids `t.Parallel`, so the right fix is the repo's existing `//nolint:paralleltest` convention with a reason only if v2.15.0 still flags them). Never relax `.golangci.yml`.

## Tests
- The five tests above pass in a full `make test` run, and under `go test -short -p 16 ./...` with concurrent CPU load, 3 runs in a row with `-count=1`.
- Each fix is validated against the reproduction in step 1: the widened-window version fails before the fix and passes after, with the same `file:line`.
- Negative control: `TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt` must still fail if the transport reuses the stranded connection (the first probe's 400 ms timeout and the `ProtoMajor`/`Len(conns, 2)` assertions stay).
- `TestLoginWithOrgSessionCapNeverExceedsIt` must still fail if the cap is not applied (expiry at login + 7 days).
- `golangci-lint run ./...` with v2.15.0 is clean.

## To verify
- Whether the Chrome-based tests share a user-data dir or debugging port (look at `Open` and `liveBrowserSettings` in `checkbrowser`).
- Which of the seven `nolintlint` findings and three `paralleltest` findings are real once on v2.15.0 (they were only seen on v2.14.0).
- Whether `go test` for `checkerdef` and `auth` contends on something shared (egress guard global state, SQLite temp dir) besides CPU.
