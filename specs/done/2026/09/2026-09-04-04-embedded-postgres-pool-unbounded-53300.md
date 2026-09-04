---
model: sonnet
effort: medium
---

# The embedded-Postgres path opens an unbounded pool against a `max_connections=10` server, so concurrent tests fail with `53300`

## Problem

`TestIncrementUsageCounter_Concurrent_Postgres`
([server/internal/db/postgres/org_usage_counter_postgres_test.go:140](server/internal/db/postgres/org_usage_counter_postgres_test.go:140))
fails reproducibly:

```
increment usage counter: FATAL: sorry, too many clients already (SQLSTATE=53300)
an unconditional increment must never be refused
```

Reproduce:

```bash
cd server && go test ./internal/db/postgres/ -run TestIncrementUsageCounter_Concurrent_Postgres -count=1 -timeout 15m
```

It fails **in isolation**, not only under parallel testcontainer load, and it fails
identically on commit `a09a718fa` — so it is pre-existing on `batch/2026-09-03` and not
caused by any recent change. Verified 2026-09-04.

### Root cause (confirmed by reading the construction path)

It is a **test-infrastructure** bug, not a product bug. The embedded server runs with
`max_connections=10` (three of which Postgres reserves for superusers), while the pool the
test drives it with is **unbounded**:

- The test constructs the service with
  `New(ctx, &Config{Embedded: true, Port: …, RunMode: runModeTest})`.
- [postgres.go:189-206](server/internal/db/postgres/postgres.go:189) — when `cfg.Embedded`
  is set, `New` forwards to `NewEmbedded` and **returns before**
  [`applyPoolLimits`](server/internal/db/postgres/postgres.go:105) is ever called. Every
  pool field on `Config` (`MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`,
  `ConnMaxIdleTime`) is silently dropped on this branch — `NewEmbedded`'s positional
  signature does not even carry them.
- [postgres.go:261](server/internal/db/postgres/postgres.go:261) — `NewEmbedded` therefore
  opens `sql.DB` with Go's default `MaxOpenConns` of **unlimited**.
- [postgres.go:251](server/internal/db/postgres/postgres.go:251) and
  [embeddedpg/embeddedpg.go:160](server/internal/db/postgres/embeddedpg/embeddedpg.go:160)
  both pin the embedded server at `"max_connections": "10"`.

50 goroutines each grabbing a connection therefore ask for ~50 backends from a server that
allows 7 non-superuser ones. The 8th onward get `53300` and the assertion — correctly —
calls that a refused increment.

**Production is not affected.** The non-embedded `New` path applies `applyPoolLimits`
([postgres.go:210](server/internal/db/postgres/postgres.go:210)) with
`SP_DB_MAX_OPEN_CONNS` (default 25), and `database/sql` queues callers beyond the ceiling
rather than failing them. `NewEmbedded` is test-only. So the "production path exhausts a
pool under concurrent rate-limit skips" hypothesis in the bug report is **ruled out** —
no product fix is needed. (This should still be re-verified, not taken on faith, before
the spec is closed.)

### Existing workaround, applied inconsistently

Three sibling suites already know about this and each hand-bound the pool after
construction:

- [tls_storage_postgres_test.go:49](server/internal/db/postgres/tls_storage_postgres_test.go:49)
  — `s.DB().SetMaxOpenConns(4)`
- [incident_number_test.go:91](server/internal/db/incident_number_test.go:91) —
  `svc.DB().SetMaxOpenConns(6)`
- [slug_race_test.go:119](server/internal/handlers/checks/slug_race_test.go:119) —
  `dbSvc.DB().SetMaxOpenConns(6)`

Every new concurrency test has to remember this ritual, and this one didn't. That is the
real defect: the safe ceiling is opt-in per test instead of being a property of the
embedded constructor.

### A second, latent instance in the same file

`TestReserveMonthlyUsage_ConcurrentNeverOverruns_Postgres`
([org_usage_counter_postgres_test.go:28](server/internal/db/postgres/org_usage_counter_postgres_test.go:28))
fires the same 50 goroutines at the same unbounded pool, but **swallows the error**
(`if reserveErr == nil && ok`). It passes today only because ≥5 of the 50 goroutines
happen to win a connection; it is a latent flake and it hides the same defect.

### Note for whoever runs the package

`go test ./internal/db/postgres/` takes **~11 minutes** and needs `-timeout 45m`. The
default 10-minute timeout kills it mid-run, and the resulting goroutine dump reads like an
assertion failure rather than a timeout.

## Proposal

Fix it once, in the constructor, rather than per test.

1. **Give the embedded path a bounded pool by default.** In
   [`NewEmbedded`](server/internal/db/postgres/postgres.go:238), apply pool limits to the
   `sql.DB` it opens — a ceiling comfortably under the embedded server's
   `max_connections=10` minus the 3 superuser-reserved slots (e.g. `MaxOpenConns: 5`,
   `MaxIdleConns: 2`). Reuse `applyPoolLimits` so there is one place that bounds a pool.
   With a bounded pool, `database/sql` queues the 50 callers and every increment lands, so
   the test asserts what it means to assert (the upsert's correctness) instead of
   accidentally asserting connection availability.

   Keep `NewEmbedded`'s callers working — it is called from many suites; prefer defaulting
   inside the function over widening its already-long positional signature.

2. **Don't let `New` silently drop pool config on the embedded branch.** Either plumb
   `cfg`'s pool fields through to the embedded construction, or (simpler, and honest) keep
   the hard-coded embedded default and add a comment at
   [postgres.go:190](server/internal/db/postgres/postgres.go:190) stating that the embedded
   branch ignores `Config`'s pool fields on purpose because the embedded server's own
   `max_connections` dictates the ceiling. Silence is what made this cost a debugging
   session.

3. **Remove the now-redundant per-test `SetMaxOpenConns` calls** in the three suites listed
   above — or leave them and confirm they are no longer load-bearing. Whichever, they must
   not end up fighting the new default (a test that raises the ceiling back above the
   server's limit reintroduces the bug).

4. **Stop the sibling test from hiding connection failures.** In
   `TestReserveMonthlyUsage_ConcurrentNeverOverruns_Postgres`, collect `reserveErr` and
   require that every error is either nil or a genuine over-limit refusal — never a
   connection error. A concurrency test that swallows infrastructure errors proves nothing.

5. **Consider raising the embedded `max_connections`** from 10 only if step 1 proves
   insufficient for some suite. Prefer bounding the client: a low server ceiling is a
   useful forcing function on dev machines, and raising it costs shared memory per test
   instance (there are many, each on its own port).

### Verification

- `cd server && go test ./internal/db/postgres/ -run 'TestIncrementUsageCounter_Concurrent_Postgres|TestReserveMonthlyUsage_ConcurrentNeverOverruns_Postgres' -count=3 -timeout 15m`
  — green three times running, no `53300`.
- Full package once: `go test ./internal/db/postgres/ -count=1 -timeout 45m` (~11 min).
- The three suites that previously self-bounded the pool
  (`internal/db`, `internal/db/postgres`, `internal/handlers/checks`) still pass.
- Confirm the production claim rather than assuming it: a test (or a documented reading of
  [postgres.go:209-210](server/internal/db/postgres/postgres.go:209)) showing the
  non-embedded path applies `MaxOpenConns` and that `database/sql` queues rather than
  errors past the ceiling. If that turns out to be false, this spec grows a product fix
  and should be re-scoped rather than closed.

## Amendment (implementer, 2026-09-04)

The "test-only / production not affected" claim above is **false**, and the spec's Proposal
and Verification sections should be read with that correction in mind.

`postgres-embedded` is a real, user-selectable `SP_DATABASE_TYPE` (documented at
`web/docs/docs/configuration/database.md` and `index.md`), reached from non-test code at:

- `server/main.go` `openDB` (~:526) — `case "postgres-embedded"` constructs
  `postgres.New(ctx, &postgres.Config{Embedded: true, Port: embeddedPostgresPort, ...})`.
- `server/main.go` `runMigrations` (~:556) — same, for `solidping migrate`.
- `server/internal/app/server.go` (~:260) — same, for the running server's `openDB`
  equivalent in `internal/app`.

None of these three call sites set `MaxOpenConns`/`MaxIdleConns` on the `Config` they build
(the embedded branch of `Config` doesn't carry them at all), and `NewEmbedded` pins
`"max_connections": "10"` on **every** path it runs, production included. So, before this
fix, an operator running `SP_DATABASE_TYPE=postgres-embedded` in a real deployment had an
unbounded client pool against a Postgres instance that hard-refuses connections past 7
non-superuser slots — a real (if niche, since embedded mode is not the recommended
production path) production failure mode, not merely a test-infrastructure one. It would
surface as hard `53300 sorry, too many clients already` errors under quite modest
concurrency, exactly like the test failure this spec set out to fix.

This does **not** change the fix itself: bounding `NewEmbedded`'s pool to `MaxOpenConns: 5`
turns those hard errors into `database/sql` queueing for the embedded path too, which is
strictly better for production embedded deployments as well as for tests. Do not revert or
weaken the bound, and do not raise the embedded server's `max_connections` to compensate —
per the original Proposal's point 5, bounding the client is preferred.

The *other* half of the original production claim **is** true and is now pinned by a test:
the non-embedded `New` path (the real recommended production path) applies
`applyPoolLimits(sqldb, cfg)` with `cfg.MaxOpenConns` (`SP_DB_MAX_OPEN_CONNS`, default 25),
and `database/sql` queues callers past that ceiling rather than refusing them —
`TestApplyPoolLimits_QueuesRatherThanErrors`
(`server/internal/db/postgres/applypoollimits_queue_test.go`) verifies this at the
`database/sql` level, driver-agnostically.

So this spec is, in the end, both a test-infrastructure fix and a product fix — filed and
scoped as the former, but landing as both. See commit `106757cb2` for the actual change;
this amendment only corrects the analysis text above it.
