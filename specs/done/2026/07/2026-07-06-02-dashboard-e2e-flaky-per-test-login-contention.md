# `dashboard.spec.ts` E2E flake: concurrent per-test UI logins overload the single-connection SQLite test DB + argon2id

## Problem

Running the full `web/dash0/e2e/dashboard.spec.ts` suite (all 13 tests in the
file together) intermittently fails 1-3 unrelated tests per run — the
specific failure varies between runs (`recent activity renders resolved
labels and activation descriptions`, `healthy org: glance card lists
checks...`, `with an active incident, the incidents card renders above the
glance card`). Every one of these passes reliably in isolation
(`bunx playwright test e2e/dashboard.spec.ts -g "<test name>"`). One observed
failure mode is `fixtures.ts:25` timing out:

```
page.waitForURL((url) => !url.pathname.includes("login"), { timeout: 10000 })
```

### Root cause: every test performs its own full UI login, and logins bottleneck on two shared, serializing resources

`web/dash0/e2e/fixtures.ts:7-36` defines `authenticatedPage` as a
**test-scoped** fixture (Playwright's default scope — no
`{ scope: "worker" }`). Every test in `dashboard.spec.ts`'s single
`test.describe("Dashboard", …)` block (`dashboard.spec.ts:110`) uses it, so
each of the 13 tests independently: navigates to `orgs/test/login`, fills the
form, submits, and waits for the redirect. `web/dash0/playwright.config.ts:6-19`
sets `fullyParallel: true` and `workers: process.env.CI ? 1 : undefined` —
locally, `undefined` means Playwright dispatches this file's 13 tests across
roughly half the machine's CPU cores as parallel workers, all against the
**one** shared server process `global-setup.ts` starts for the entire run
(`global-setup.ts:142-152`, not per file or per test). Neither of the other
three files that already hit login-related flakiness workarounds
(`badges.spec.ts:52`, `check-labels.spec.ts:4`, `server-admin.spec.ts:141`,
all via `test.describe.configure({ mode: "serial" })`) fixed the underlying
cause — they just serialize one file's own tests.

That means a burst of up to ~13 concurrent real logins can hit the backend
at once. Two things in the login path serialize under that burst:

**1. Argon2id verification is real and CPU-bound, with a small global
concurrency cap.** The seeded E2E user's password is not a shortcut —
`server/test/testdata/testdata.go:171,179` calls `passwords.Hash("test")`,
producing a genuine argon2id hash at the default cost profile
(`server/internal/config/config.go:679-685`: `m=64 MiB, t=3, p=4`). Every
login's `passwords.Verify` (`server/internal/handlers/auth/service.go:462`)
re-derives that hash. `server/internal/utils/passwords/argon2id.go:28-46`
bounds concurrent derivations process-wide to
`min(GOMAXPROCS, 4)` (`hashSem`) specifically to cap peak memory
(`memory × N`) — the comment there is explicit that "excess concurrent auth
requests queue briefly rather than all allocating at once." Under 13
near-simultaneous logins, several queue behind that semaphore, and each
derivation is CPU-heavy enough that queueing adds up, especially while the
same cores are also running Playwright's own worker/Chromium processes.

**2. The E2E server actually runs SQLite with exactly one open connection —
not the Postgres `global-setup.ts` starts.** `global-setup.ts:130-152` starts
Postgres via `docker compose up -d postgres` and launches the server with
`SP_RUNMODE=test`, but sets no database type. `server/internal/config/config.go:839-842`:

```go
// When in test mode and no database type is specified, default to sqlite-memory
if cfg.RunMode == "test" && cfg.Database.Type == "" {
    cfg.Database.Type = DatabaseTypeSQLiteMemory
}
```

Neither `server/config.yml` nor `server/config.local.yml` sets a database
type, so this default fires: the local E2E run's backend is SQLite-in-memory,
and the Postgres container `global-setup.ts` pays ~5s to start is unused.
`server/internal/db/sqlite/sqlite.go:125` then caps the pool at
`sqldb.SetMaxOpenConns(1)` ("SQLite performs better with a single writer") —
**every** DB operation on the whole server, for the whole run, serializes
through that one connection. `completeLogin`
(`server/internal/handlers/auth/service.go:539`) does several sequential DB
round trips per login (user/org lookup, last-active update, refresh-token
insert); 13 concurrent logins queue those round trips one at a time behind
the same connection.

**3. Three compounding factors extend the window further.** None of these are
required for the theory to hold, but each adds to the same queue:

- **The per-IP rate limiter is live locally, not just in CI.**
  `server/internal/middleware/ratelimit.go` is mounted globally
  (`server/internal/app/server.go:405-409`) and applies to
  `/api/v1/auth/login` (not in the exemption list at `ratelimit.go:33-36`).
  It's scoped **per IP** (`ratelimit.go:75-117`), and every Playwright worker
  originates from `localhost` — so all concurrent logins share one bucket
  (defaults: `MaxConcurrent: 20`, `ConcurrencyQueue: 10`,
  `MaxQueueWait: 30s`, `config.go:648-655`). CI explicitly zeroes this out
  (`.github/workflows/ci.yml:298-299`); the local `make dev-test` /
  `global-setup.ts` path does not.
- **The frontend can add its own second serialized round-trip before
  redirecting.** `applyLoginResponse`
  (`web/dash0/src/context/AuthContext.tsx:201-243`) awaits a `GET
  /api/v1/auth/me` (`AuthContext.tsx:235`) when the login response's
  `organizations` array is empty — plausible for the single-org seeded test
  user. `login.tsx:286-296`'s `navigate()` can't fire until that second
  request also clears the same single SQLite connection, so the redirect
  the fixture is waiting on depends on two serialized backend round trips,
  not one.
- **Session-cap enforcement contends on the same user's rows.**
  `completeLogin`'s `enforceSessionCap`
  (`server/internal/handlers/auth/service.go:602,634-667`) does a `SELECT`
  (and possibly a `DELETE`) over the logging-in user's existing token rows.
  Every `dashboard.spec.ts` test authenticates as the *identical* seeded
  user (`test@test.com`), so concurrent logins don't just share a DB
  connection in the abstract — they repeatedly read and mutate the same
  user's session rows.

None of the individual factors would necessarily blow a 10s budget alone,
but stacked together — semaphore-gated CPU-bound hashing, single-connection
DB writes, a shared-bucket rate limiter, a conditional second network round
trip, and same-user session-row contention, all triggered by tests that
don't need to log in independently in the first place — the tail of the
queue can occasionally exceed the fixed 10s `waitForURL` timeout. This also
explains why it's never the same test that fails: whichever login happens to
land at the back of the queue at that moment loses, and an isolated `-g` run
never queues at all (exactly one login in flight).

## Considered alternatives

- **Just raise the `waitForURL` timeout.** Rejected per the investigation
  ask: it doesn't address the queueing, just moves the threshold, and masks
  a real (if currently harmless) inefficiency — 13 redundant real logins per
  file purely to reach an authenticated page.
- **Add `test.describe.configure({ mode: "serial" })` to `dashboard.spec.ts`**,
  mirroring the existing workaround in three other files. This would fix the
  *reported* repro (this file run alone), but the underlying fixture still
  does one real UI login per test, and a full `make test-dash` run still lets
  other spec files' logins overlap with whichever file is serialized — the
  systemic cause would keep resurfacing file by file, as it evidently already
  has three times.
- **Fix only the Postgres/SQLite mismatch** (make test mode actually use the
  Postgres `global-setup.ts` starts, raising the DB connection ceiling from 1
  to the Postgres pool default of 25, `config.go:36-39`). This relieves the
  dominant DB-serialization cause, but the argon2id `hashSem` cap (4), the
  shared-bucket rate limiter, and the sheer redundancy of 13 independent
  logins remain — and it doesn't touch the pattern other spec files will
  keep hitting. Two independent passes over this investigation both flag the
  DB mismatch as the single biggest contributor, so it should not be treated
  as optional cleanup — see proposal A below.

## Proposal

Both of the following are needed: A removes the dominant structural cause,
B removes the redundant load that triggers it in the first place. Either one
alone plausibly reduces the flake rate; both together is the actual fix.

### A. Wire test-mode E2E runs to the Postgres `global-setup.ts` already starts

`global-setup.ts:130-140` pays the cost of starting Postgres via
docker-compose and then never points the server at it
(`global-setup.ts:144-152` only sets `SOLIDPING_LISTEN`, `SP_RUNMODE`,
`SP_DB_RESET`), so `config.go:839-842`'s `RunMode == "test"` default
silently selects `sqlite-memory` — and with it, `sqlite.go:124-127`'s
hardcoded `SetMaxOpenConns(1)`. Pass through the Postgres connection info
(`SP_DB_TYPE=postgres` + the docker-compose service's `SP_DB_URL`/host/port)
so the E2E server actually gets the Postgres pool (25 max / 10 idle,
`config.go:36-39`) instead of a single shared connection. Apply the same fix
to the CI workflow if it has the same gap (`.github/workflows/ci.yml`
already starts Postgres for E2E — verify it wires the connection through,
since `ci.yml:298-299` only shows the rate-limiter env being zeroed, not a
DB-type override).

This alone raises the concurrent-request ceiling ~25x for every DB-backed
request on the server, not just login — the biggest single lever available.

### B. Authenticate once per worker instead of once per test

Convert `authenticatedPage` to authenticate once per **worker** instead of
once per **test**, following Playwright's documented "authenticate once,
reuse `storageState`" pattern. Concretely, in
[fixtures.ts](web/dash0/e2e/fixtures.ts):

- Add a worker-scoped fixture that performs the current UI login flow
  exactly once per worker process and captures
  `page.context().storageState()`.
- Have `authenticatedPage` (test-scoped, as consumers expect today) create a
  fresh browser context from that cached storage state instead of driving
  the login form again, then hand back a page from it.
- This caps real, concurrent logins at `workers` (≤ hashSem's cap of 4 in the
  common case) instead of at `tests-in-flight`, removes 12 of 13 redundant
  logins per file, and — since all logins are for the same seeded user —
  eliminates the `enforceSessionCap` same-user-row contention entirely for
  every test after the first per worker. The login form still gets
  first-class coverage from `login.spec.ts`.
- Confirm token lifetimes make this safe: `AccessTokenExpiry` is 1h and
  `RefreshTokenExpiry` is 7 days (`server/internal/config/config.go:668-669`),
  comfortably outliving a worker's test run; the dashboard already has
  silent token refresh (spec `2026-07-05-11`), so a long-lived cached session
  should behave like a real returning user.

## Out of scope

- Changing argon2id cost parameters or the `hashSem` concurrency cap — those
  are appropriately tuned for production password security; the fix reduces
  test-side login volume, not hashing strength.
- Auditing/migrating the three existing `describe.configure({ mode: "serial" })`
  workarounds (`badges.spec.ts`, `check-labels.spec.ts`,
  `server-admin.spec.ts`) — leave them as-is unless they're later observed
  to still flake after this fix.
- Fixing flakiness in any other spec file not yet observed to hit this
  pattern — they inherit the fix automatically once `fixtures.ts` changes,
  since they all consume the same `authenticatedPage` fixture.

## Acceptance criteria

- The local E2E server (`global-setup.ts`) runs against the Postgres
  container it starts, not the silent `sqlite-memory` default — confirmed by
  checking the running server's active DB driver/pool size during a test
  run (e.g. via startup log line or `pg_stat_activity`).
- CI's E2E job is verified to already do this (or is fixed to match) — not
  left as an unreviewed assumption.
- `authenticatedPage` drives the UI login form at most once per Playwright
  worker, not once per test.
- `bunx playwright test e2e/dashboard.spec.ts` run repeatedly back-to-back
  locally (e.g. `--repeat-each=20`) produces zero failures attributable to
  the `waitForURL` timeout in `fixtures.ts`.
- Full `make test-dash` remains green, including specs that intentionally
  need a genuinely fresh/unauthenticated page (`login.spec.ts`,
  `invitations.spec.ts`, `session-continuity.spec.ts`, `sessions.spec.ts`,
  etc.) — confirmed by audit that they don't rely on `authenticatedPage`, or
  updated if they do.
- No change to production `argon2id`/`bcrypt` parameters, the `hashSem`
  concurrency cap, or the rate limiter's production defaults.

## Implementation plan

- [ ] A: pass Postgres connection info (`SP_DB_TYPE`/`SP_DB_URL` or
      equivalent) through to the server process in `global-setup.ts:144-152`
      so `SP_RUNMODE=test` no longer falls into the `sqlite-memory` default
      at `config.go:839-842`.
- [ ] A: audit `.github/workflows/ci.yml`'s E2E job for the same gap — confirm
      it already wires the DB type/URL through, or fix it alongside.
- [ ] A: confirm `SP_DB_RESET=true` still resets a Postgres DB correctly
      between full local runs (it's currently only exercised against SQLite
      in this path).
- [ ] Audit every `e2e/*.spec.ts` file's usage of `authenticatedPage` vs.
      tests that deliberately want a fresh/unauthenticated page, so the
      fixture redesign doesn't break intentionally-unauthenticated flows.
- [ ] B: redesign `fixtures.ts`: worker-scoped login fixture caching
      `storageState`; test-scoped `authenticatedPage` builds a context from
      that cached state instead of re-running the login UI flow.
- [ ] B: sanity-check token TTL assumptions against real worker run
      durations (spot check `make test-dash` wall-clock time vs.
      `AccessTokenExpiry`).
- [ ] Run `e2e/dashboard.spec.ts` with `--repeat-each=20` (or a local loop)
      to confirm the flake is gone, ideally testing A and B independently
      first (to confirm both mattered) before landing both together.
- [ ] Run full `make test-dash` to confirm no regressions from either change
      across all spec files.
