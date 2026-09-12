# Go test layers — what runs where

`make test` does not test PostgreSQL. It never has. If you are here because you
are about to trust it, read the next two sections and stop.

## The short version

| Layer | Command | Runs on a PR? | What it covers |
|---|---|---|---|
| **Short** | `make test` (`go test ./... -short`) | Yes — `backend-lint` job | Everything that needs no database beyond SQLite. ~1 min. |
| **Postgres** | `make test-postgres` | Yes — `backend-postgres` job | Every `*_postgres_test.go` + every other `testing.Short()`-guarded suite. Embedded PostgreSQL, ~12 min. |
| **Slow** | `make test-slow` (`-tags slowtests`) | **No** — nightly only | Live WHOIS/RDAP, a live TLS dial, and the Pebble ACME suite (Docker). |
| **E2E** | `cd web/dash0 && bun run test:e2e` | Yes — `e2e-tests` job | Playwright against a built binary + a Postgres service container. See [e2e-ci.md](e2e-ci.md). |

## Why the layers exist

`-short` used to guard three unrelated needs at once:

1. needs an embedded PostgreSQL,
2. needs the public internet (`checkdomain` looked up a real domain by WHOIS,
   `checkssl` dialed a real host),
3. needs a Docker daemon (`tlsedge`'s Pebble ACME end-to-end suite).

CI's only Go test invocation was `go test -count=1 ./... -short`, so **the only
database CI had ever exercised was SQLite** — while production runs Postgres.

That is not theoretical. Spec `2026-09-11-03` shipped the org-parameters API
with a key pattern allowing hyphens, against a Postgres `CHECK` constraint that
had refused hyphens since the released `001` baseline. SQLite's `parameters`
table carried no such CHECK, so all four of the feature's tests passed, three
independent audit rounds certified it clean, and the feature's own documented
flagship example — `PUT /api/v1/orgs/:org/parameters/sso-authtest-password` —
answered HTTP 500 in production. Two of the four tests written *specifically*
to catch that bug were `--- SKIP` in CI, and `go test` printed `ok`.

Because dropping `-short` would have switched all three needs on at once,
spec `2026-09-12-05` separated them: needs 2 and 3 moved behind a
`//go:build slowtests` tag, and everything merely Postgres-backed kept only its
`testing.Short()` guard — so it can now run on every PR without dragging
registrar flakiness or a container pull into the merge path.

## Running the Postgres layer locally

```bash
make test-postgres
```

which is exactly:

```bash
cd server && SP_TEST_REQUIRE_POSTGRES=1 go test -count=1 -p 1 -v ./...
```

Three parts, none of them optional:

- **no `-short`** — that is the whole point.
- **`-p 1`** — not a performance knob. `embedded-postgres-go` keeps its
  extracted binaries *and its `pwfile`* in one shared
  `~/.embedded-postgres-go/extracted`, so two instances starting concurrently
  make the loser fail with "unable to remove password file". Suites are serial
  *within* a package (`//nolint:paralleltest`), but `go test ./...` runs
  *packages* concurrently at `-p` = GOMAXPROCS. The full suite also flakes under
  parallel testcontainer load.
- **`SP_TEST_REQUIRE_POSTGRES=1`** — see below.

First run downloads a PostgreSQL distribution into `~/.embedded-postgres-go`;
after that it is cached (CI caches the same directory with `actions/cache`).

To run one package while iterating:

```bash
cd server && SP_TEST_REQUIRE_POSTGRES=1 go test -count=1 -v ./internal/handlers/orgparams/
```

## `SP_TEST_REQUIRE_POSTGRES` — why a skip must be a failure

Every Postgres-backed suite in the tree ends its startup guard the same way:

```go
svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: ...})
if err != nil {
    testsupport.PostgresUnavailable(t, err)  // was: t.Skipf(...)
}
```

Skipping is right on a laptop with no Postgres binaries downloaded. It is
exactly wrong in CI: a job whose entire purpose is to exercise Postgres would
report `ok` having proven nothing the moment the binary download is blocked, a
port clashes, or two packages race over the shared `pwfile`. A CI job built on
skip-on-failure does not catch the bug above — it *reproduces* it.

So `internal/testsupport` reads `SP_TEST_REQUIRE_POSTGRES`; when it is set,
`PostgresUnavailable` and `PostgresInitFailed` call `t.Fatalf` instead of
`t.Skipf`. CI sets it. Local runs leave it unset and keep skipping.

`TestMain` has no `*testing.T` and so cannot reach `t.Fatalf`; the variant for
that shape is `testsupport.PostgresUnavailableTestMain(os.Stderr, err)`, which
reports whether the caller must `os.Exit(1)` instead of running the suite empty.
`server/test/integration/scenario` is its one caller — it boots a shared
embedded Postgres for seven scenarios and used to run them all as skips when
that failed.

Never "fix" a red `backend-postgres` job by unsetting the variable. A red job
there means embedded Postgres could not start, and the next thing that fails
silently will be a real regression.

## Belt and braces: the CI guard step

`go test` prints `ok` for a package whose every test skipped, so exit status
alone is not evidence. Both `backend-postgres` (in `ci.yml`) and the nightly
Postgres job parse the `-v` output and fail if:

- any `--- SKIP` line names a Postgres test, or
- the log contains `testsupport`'s own skip wording (which can only appear if
  `SP_TEST_REQUIRE_POSTGRES` never reached the test binary — this one does not
  depend on how the test is named), or
- fewer than **100** top-level `--- PASS` lines name one (116 such tests exist
  as of spec `2026-09-12-05`).

If you add or remove Postgres suites in bulk, that floor is the number to
revisit — raise it when the count grows; lower it only alongside a real
deletion, never to make a red job green.

## The `slowtests` layer

Four suites, all excluded from `make test`, `make test-postgres` and every
per-PR job:

| File | Needs |
|---|---|
| `internal/checkers/checkdomain/checker_live_test.go` | Public internet (real WHOIS/RDAP) |
| `internal/checkers/checkssl/checker_live_test.go` | Public internet (real TLS dial) |
| `internal/tlsedge/acme_e2e_test.go` | Docker (Pebble ACME CA) |
| `internal/tlsedge/pebble_test.go` | Docker (plumbing for the above) |

```bash
make test-slow
```

They run in `.github/workflows/nightly.yml` (03:17 UTC, plus
`workflow_dispatch`), which asserts all four PASS by name — a Docker-less runner
would otherwise skip them into invisibility.

`golangci-lint` is configured with `build-tags: [slowtests]`, so these files are
still linted on every PR even though they are not compiled into any PR test run.

Note: `internal/db/postgres/support_inbox_migration_postgres_test.go` mentions
`testcontainers` in a comment but does **not** use it — it is a pure text
assertion over a shipped `.sql` file, needs nothing, and deliberately stays in
the PR layer.

## Adding a test — which layer?

- **Needs no database, or SQLite only** → nothing to do; it runs everywhere.
- **Needs PostgreSQL** → guard with `testing.Short()` and route the startup
  failure through `testsupport.PostgresUnavailable` /
  `testsupport.PostgresInitFailed` (or `PostgresUnavailableTestMain` from a
  `TestMain`) — never a bare `t.Skipf`, whatever the wording. Claim a port number no other
  `*_postgres_test.go` uses (see the port-numbering note in
  `internal/db/postgres/postgres_headroom_postgres_test.go`). Prefer one shared
  instance per package via `sync.Once` + `TestMain` —
  `internal/handlers/orgparams/service_postgres_test.go` is the worked example,
  and explains what happens when you do not.
- **Needs the public internet or Docker** → put it in its own file with
  `//go:build slowtests`, and add it to the nightly workflow's by-name PASS
  assertion.

## Known gap

The Postgres layer costs ~12 minutes per PR because embedded PostgreSQL boots
once per *suite function* rather than once per run — 83 `Embedded: true` call
sites each start their own instance. Rewiring those helpers to accept an
external DSN would let one Postgres serve every suite and cut that
substantially. Spec `2026-09-12-05` deliberately deferred it; it is a
worthwhile follow-up, not a blocker.
