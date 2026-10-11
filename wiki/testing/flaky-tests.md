# Flaky tests are bugs

A test that fails sometimes is reporting a race, a leak, an unrecovered panic or an
order dependency. Re-running until green ships that bug. Root-cause every
intermittent failure, including CI-only ones.

## Rules

- Do not stop at "CI-only flake". Add the observability that captures evidence next
  time (keep the server log, do not `& disown` it).
- Prefer guards that turn a silent flake into a hard signal (panic recovery so one bad
  request 500s instead of killing the server and cascading connection-refused errors).
- Test-order pollution is a bug in the polluting test. Fix the leak, never loosen the
  victim.
- A conditional `test.skip(...)` that skips when a fixture is missing can hide a broken
  seed.
- A test asserting an absence ("the secret is not in the logs") needs a positive
  control first ("the log line is captured"), or it passes vacuously.
- A fake server must be at least as strict as the real one. The Slack fakes once
  accepted JSON bodies on `conversations.list`, which real Slack silently ignores
  (it wants form encoding), so a green test proved nothing.

## Techniques

**Widen the window.** A test that times out on its full deadline, rather than running
slow, usually lost a notification (subscribe after announce). Prove it with controls,
not re-runs:

1. Add a `time.Sleep` that widens the suspected window in the unmodified code. It must
   reproduce CI's failure exactly (same `file:line`, same message).
2. Apply the fix, keep the sleep. It must pass.

Passing N times in isolation proves nothing (one such test passed 5 times locally
before its fix).

**Wall-clock landmines.** A "failed once under load" test may only fail during one
second of the minute, for example when a hash of a hard-coded UID sets a schedule
phase. Tells: a failure margin of nanoseconds over an exact bound, every concurrent
instance failing in the same second, a function's internal clock compared to a
`time.Now()` sampled earlier. Reproduce by starting the test aligned just before the
suspect second, not by stressing the machine. Fix by bounding against a clock sampled
after the call.

**Pre-existing `-race` findings.** Restore the committed file and re-run before
blaming your change. CI runs `go test -short` without `-race`, so a race can be real
and still not be what CI is failing on.

## Known shapes in this repo

- **Postgres testcontainers under parallel load.** A full local `go test ./...` can fail
  a scattered set of Postgres, LDAP and PG-notify tests from container contention.
  Re-run the failing packages with `go test -p 1 <pkgs>`. If they pass serially it was
  contention, which does not make it fine: CI's Postgres layer runs `-p 4`
  (`make test-postgres`), so the same contention can reach it. A per-package timeout
  there is often intra-package parallelism on a slow runner: `internal/db/postgres`
  takes ~3 min locally against Go's default 10 min per-package budget.
- **`internal/config` is hostname-independent.** Hostnames like `Host-002.lan` are
  slugified now, so a red `internal/config` is a real regression, not the machine.
  Check with `env -u SP_NODE_NAME go test ./internal/config/`. New tests there must
  inject the hostname (see `TestSanitizeHostnameSlug_FixedPointProperty`).
- **Hand-built middleware fixtures.** Several handler tests compose their own
  middleware chain and claim to mirror `server.go`. Adding a middleware to the real
  chain leaves them green while they assert the old behaviour. When you add one, grep
  them and update them in the same change:
  `grep -rn "Use(authMw\.\|Use(authMW\.\|RequireAuth(authMw\|Shared:.*RequireAuth" --include=*_test.go server/`.
  Turn an assertion that a role got through into a refusal test plus a positive control
  at the lowest role that still passes.

E2E-specific traps (side-car env, paging, shared query keys) are in
[local-e2e.md](local-e2e.md).
