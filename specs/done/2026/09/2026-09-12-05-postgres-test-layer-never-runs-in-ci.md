---
model: opus
effort: high
---

# The entire Postgres-backed Go test layer never runs in CI, so an engine-specific regression can ship green

## Problem

CI's only Go test invocation is `-short`:

- `.github/workflows/ci.yml:164` → `go test -count=1 ./... -short`
- `Makefile:337` (`make test`) → `cd server && go test ./... -short`

72 test files across 21 packages under `server/internal/` call `testing.Short()`
to self-skip. That includes every `*_postgres_test.go` (42 files in
`server/internal/db/postgres/` alone, 83 `Embedded: true` call sites tree-wide),
the `testcontainers`-backed migration test, and the Docker-backed ACME suite. So
the only database any CI-verified Go test has ever exercised is SQLite.

### What it cost

Spec `2026-09-11-03` shipped the org-parameters API with a key pattern that
allows hyphens (`internal/paramkeys`: `^[a-z][a-z0-9_.-]{0,63}$`), while Postgres
has constrained `parameters.key` to `^[a-z0-9_\.]+$` since the released
`001_v0_1_0` baseline. SQLite's `parameters` table carried no such CHECK, so
every test for the feature passed, and three independent audit rounds certified
it clean. `PUT /api/v1/orgs/:org/parameters/sso-authtest-password` — the
feature's own documented flagship example — returned HTTP 500 on Postgres. It
surfaced only in a full dash0 Playwright run against a Postgres side-car, after
the spec had already been archived.

### The failure is reproducible in one command

```
$ cd server && go test -count=1 -v -short ./internal/handlers/orgparams/
--- PASS: TestOrgParametersRoundTripOnSQLite (0.22s)
--- PASS: TestPlatformParameterKeysSurviveOnSQLite (0.22s)
--- SKIP: TestOrgParametersRoundTripOnPostgres (0.00s)
--- SKIP: TestParameterKeyConstraintOnPostgres (0.00s)
ok
```

Two of the four tests in the file written *specifically* to catch this bug do not
run in CI, and `go test` still prints `ok`. Without `-short`, all four report
`--- PASS`.

### Measured runtime (this laptop, warm build cache)

| Invocation | Wall time |
|---|---|
| `go test ./internal/handlers/orgparams/` (2 embedded-PG tests, one shared instance) | **13.7s** |
| `go test -p 1 ./internal/db/postgres/` (42 `*_postgres_test.go`, ~1 embedded boot per suite) | **618.8s (10m19s)** |

The cost is almost entirely embedded-Postgres startup, paid once per suite
function rather than once per package. `internal/db/postgres` is the whole
budget; the other 20 packages are individually cheap.

### Three traps any design has to survive

1. **`-short` is overloaded.** It guards three unrelated things, so a blanket
   non-short run enables all of them:
   - embedded-Postgres suites (the layer we actually want),
   - **live-network** tests — `internal/checkers/checkdomain/checker_test.go:98`
     does a real WHOIS lookup against `google.com`,
   - **Docker-dependent** suites — `internal/tlsedge/acme_e2e_test.go` boots a
     Pebble CA container, `internal/db/postgres/support_inbox_migration_postgres_test.go`
     uses `testcontainers`.
2. **Every helper skips on startup failure.** The pattern is
   `t.Skipf("embedded postgres unavailable: %v", err)`. A CI environment where
   embedded Postgres cannot start — binary download blocked, port clash, pwfile
   race — reports `ok` having proven nothing. A job built on this without a
   guard is decoration, and reproduces the original bug exactly.
3. **The shared-`pwfile` race.** embedded-postgres-go keeps its extracted
   binaries and its `pwfile` in one shared `~/.embedded-postgres-go/extracted`,
   so two instances starting concurrently make the loser fail with "unable to
   remove password file" — which trap 2 then converts into a silent SKIP. Within
   a package the suites are `//nolint:paralleltest` and therefore serial, but
   `go test ./...` runs *packages* concurrently at `-p` = GOMAXPROCS, so 21
   packages race each other. `server/internal/handlers/orgparams/service_postgres_test.go`
   documents the trap in detail and works around it with `sync.Once` +
   `TestMain`; **read that file first.** The full suite also already flakes under
   parallel testcontainer load, so `-p 1` is likely required.

## Proposal

Recommended: **a dedicated per-PR CI job that runs the Postgres layer non-short
with `-p 1`, with skips turned into hard failures**, plus a build-tag split that
moves the network/Docker suites to a scheduled run. Nightly-only is the cheaper
alternative but it lets a Postgres-only regression land and then attributes it to
whichever commit happens to sit in the nightly window — the failure mode we just
paid for, delayed rather than prevented.

Do **not** delete the `testing.Short()` guards; `make test` must stay fast.

1. **Separate the two axes `-short` currently conflates.** Put the suites that
   need the public internet or Docker behind a build tag (e.g.
   `//go:build slowtests`): the `checkdomain` live WHOIS, `tlsedge`'s Pebble
   ACME E2E, the `testcontainers` migration test. Everything merely
   Postgres-backed keeps only its `testing.Short()` guard, so it becomes
   runnable on every PR without dragging network flakiness in.

2. **Make a skip a failure when CI asks for it.** Add one shared helper (e.g.
   `internal/testsupport.RequirePostgres(t)`) that reads an env var such as
   `SP_TEST_REQUIRE_POSTGRES=1` and turns every `embedded postgres unavailable`
   path into `t.Fatalf` instead of `t.Skipf`. Convert the guarded packages to
   it. **This is the part that makes the job trustworthy** — without it the job
   can be green while proving nothing.

3. **Add a `backend-postgres` CI job** (`needs: [changes]`, gated on
   `needs.changes.outputs.code == 'true'` like its siblings) running
   `go test -count=1 -p 1 -v ./...` with no `-short` and
   `SP_TEST_REQUIRE_POSTGRES=1`. Budget ~15 min from the measured 10m19s plus
   headroom; set `timeout-minutes` accordingly. Cache
   `~/.embedded-postgres-go` with `actions/cache` so the Postgres binary
   download isn't paid per run. Keep the embedded path — the 83 `Embedded: true`
   call sites hard-code it, and rewiring them to consume the E2E job's Postgres
   service container (`ci.yml:440-465`) is a much larger change that this spec
   does not require.

4. **Assert non-emptiness, not just exit status.** Parse the `-v` output in a
   guard step: fail the job if any `--- SKIP` line appears for a Postgres test,
   and/or assert a floor on the number of `--- PASS` lines. A design that lets a
   skip look like a pass reproduces the original bug.

5. **Keep `make test` on `-short`.** Add `make test-postgres` that mirrors the
   new CI invocation exactly, so the layer is one command locally.

6. **Nightly `schedule:` workflow** for the build-tagged network/Docker layer.

Verification for whoever implements this: run the new job's exact invocation and
show `--- PASS` lines for the `*_postgres_test.go` tests, not `--- SKIP` —
including `TestOrgParametersRoundTripOnPostgres`, the test that would have caught
the hyphen bug. A green exit code is not evidence.

### Deliverable

- The CI change (`.github/workflows/ci.yml`, plus the scheduled workflow).
- The `Makefile` target.
- `wiki/testing/test-layers.md` — which test layer runs where (PR CI / nightly /
  local only), and how to run the Postgres layer locally — indexed in
  `wiki/README.md` alongside `testing/e2e-ci.md`. The next person must not
  assume `make test` covers Postgres.

### Open questions

- Scope the job to `./...` with `-p 1` (simple, pays `-p 1` on every fast package
  too) or to the 21 guarded packages (faster, needs a list that can rot)? Prefer
  `./...` unless the measured cost is unacceptable.
- Rewiring the embedded-Postgres helpers to accept an external DSN would let one
  Postgres serve every suite and cut the 10-minute figure substantially. Worth
  it, but larger — propose as a follow-up spec rather than folding it in here.

## Implementation Plan

1. **`internal/testsupport`** — a new non-test package holding the shared guard:
   `RequirePostgres()` reads `SP_TEST_REQUIRE_POSTGRES`, and
   `PostgresUnavailable(t, err)` / `PostgresInitFailed(t, err)` turn the two
   existing skip paths into `t.Fatalf` when it is set, `t.Skipf` otherwise.
   Unit-tested for BOTH directions (skip when unset, fail when set) so the
   helper has actually been observed to fail.

2. **Convert every call site** — 84 `embedded postgres unavailable` and 80
   `embedded postgres init failed` `t.Skipf`s across ~70 files, mechanically
   rewritten to the helper with the import added.

3. **Build-tag split (`//go:build slowtests`)** for the suites that need the
   public internet or Docker, so the non-short Postgres run does not drag
   network flakiness in:
   - `internal/checkers/checkdomain` live WHOIS → moved to `checker_live_test.go`
   - `internal/checkers/checkssl` live TLS dial → moved to `checker_live_test.go`
     (same class as the WHOIS test; the spec's list missed it)
   - `internal/tlsedge/acme_e2e_test.go` + its Docker plumbing `pebble_test.go`
   - `internal/db/postgres/support_inbox_migration_postgres_test.go` is named by
     the spec but does NOT need Docker: it is a pure text assertion over the
     shipped migration file ("no database needed, so this runs everywhere" —
     its own header comment; the `testcontainers` hit in the file is that
     comment, and the dependency is not in `go.mod`). Tagging it out would
     delete PR coverage for nothing, so it deliberately stays in the PR layer.

4. **`backend-postgres` CI job** in `.github/workflows/ci.yml`: `needs: [changes]`,
   same `needs.changes.outputs.code == 'true'` gate as its siblings,
   `go test -count=1 -p 1 -v ./...` with no `-short` and
   `SP_TEST_REQUIRE_POSTGRES=1`, `timeout-minutes: 20`, `actions/cache` on
   `~/.embedded-postgres-go`. Embedded path kept.

5. **Non-emptiness guard step** parsing the `-v` output: fail if any top-level
   `--- SKIP` names a Postgres test, and assert a floor on the number of
   top-level `--- PASS` lines for Postgres tests.

6. **`make test-postgres`** mirroring the CI invocation exactly; `make test`
   stays `-short`.

7. **Nightly `.github/workflows/nightly.yml`** running the `slowtests` layer
   plus the Postgres layer on a schedule.

8. **`wiki/testing/test-layers.md`** documenting which layer runs where and how
   to run each locally, indexed in `wiki/README.md`.
