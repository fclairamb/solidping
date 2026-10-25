# Running dash0 E2E locally

How to run the Playwright suite on a laptop without disturbing the developer's
`make dev` on `:4000`, and the traps that each produce a convincing false red run.
CI's own setup is in [e2e-ci.md](e2e-ci.md).

## Rules

- Never stop or `pkill` the `:4000` devloop (`pkill -f 'solidping serve'` kills it, and
  `devloop` does not bring it back). Run a side-car server on another port and kill it
  by the PID you recorded.
- Before trusting any result from a server, `curl <base>/api/mgmt/version` and check the
  commit. Ports get reused between experiments: an old build has none of the new
  frontend, so a test written for it passes there (false green) or fails (false red).
- A full suite run must finish in under 1 hour. ~850 tests pass well under that. A run
  trending past an hour is mass failure multiplied by `retries: 2`, usually a harness
  problem (base URL, stale embed, wrong env). Stop it and check the harness first.

## Side-car recipe (full suite, Postgres)

Copy it literally. `build-dash0` + `build-backend` reads complete and is not.

```bash
make build-dash0 copy-dash0 && make build-backend        # order matters, see trap 1
nc -z localhost 55432 || docker-compose up -d postgres   # the repo's solidping-postgres
docker exec solidping-postgres psql -U postgres -c "DROP DATABASE IF EXISTS solidping_e2e WITH (FORCE)"
docker exec solidping-postgres psql -U postgres -c "CREATE DATABASE solidping_e2e OWNER solidping"
mkdir -p /tmp/e2e-sidecar
SP_RUNMODE=test SP_DB_RESET=true SP_DB_TYPE=postgres \
  SP_DB_URL="postgres://solidping:solidping@localhost:55432/solidping_e2e?sslmode=disable" \
  PORT=4300 SP_DB_MIGRATION_GUARD_MODE=warn \
  SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE=0 SP_SERVER_RATE_LIMITING_MAX_CONCURRENT=0 \
  SP_AUTH_REGISTRATION_EMAIL_PATTERN='.*' SP_PLUGINS_DIR="$PWD/plugins" \
  ./solidping serve > /tmp/e2e-sidecar/server.log 2>&1 &
echo $! > /tmp/e2e-sidecar/pid
curl -s http://localhost:4300/d/ | grep -o '<title>[^<]*</title>'   # must say SolidPing
cd web/dash0 && CI=true E2E_BASE_URL=http://localhost:4300/d/ bun run test:e2e
kill "$(cat /tmp/e2e-sidecar/pid)"
```

`CI=true` makes `e2e/global-setup.ts` skip building and starting its own server, and
gives CI's retries. `playwright.config.ts` and `global-setup.ts` both honour
`E2E_BASE_URL`. To mirror CI's sharding, see the `E2E_BASE_URLS` example in
[e2e-ci.md](e2e-ci.md).

## Traps

1. **`make build-backend` does not rebuild or re-embed dash0.** It compiles Go against
   whatever sits in `server/internal/app/dash0res/`. A spec that adds a `data-testid`
   then looks like a missing feature. Run `build-dash0 copy-dash0` first, and before
   believing a red run, check the embed: `grep -rl '<new-testid>' server/internal/app/dash0res/`.
   Same for a side-car built for an earlier experiment.
2. **`E2E_BASE_URL` must end in `/d/`.** With a bare origin every relative navigation
   lands on the server's 404 page. The signature: every browser test red, the few
   pure-API tests green.
3. **Match CI's server env.** Without the two rate-limiting zeros,
   `magic-wand.spec.ts` gets 429s (it fires 100+ concurrent creates). Without
   `SP_AUTH_REGISTRATION_EMAIL_PATTERN='.*'`, `confirm-registration-no-org.spec.ts`
   cannot register, and setting it at runtime does nothing (read once at boot). When a
   suite is red locally and green in CI, diff your server env against
   `.github/workflows/ci.yml` before reading any application code.
4. **Force Postgres.** `SP_RUNMODE=test` with no `SP_DB_TYPE` can end up on SQLite,
   which serializes every request on one connection. The suite was moved off it.
5. **Use `solidping_e2e`, never `solidping`.** `SP_DB_RESET=true` against the shared dev
   database wipes local data. Drop and recreate the dedicated database per run.
6. **A foreign container may answer to `postgres`.** Other projects on the same laptop
   run their own Postgres. `role "solidping" does not exist` is the giveaway. The
   repo's service is container `solidping-postgres` on port `55432`.

## Fast iteration against `make dev-test`

For one spec file while developing:

- `make dev-test` (test mode on `:4000`, dash0 Vite on `:5174`), then
  `cd web/dash0 && bun run test:e2e:dev --workers=1`. Parallel logins trip the login
  rate limiter.
- `SP_DB_RESET=true make dev-test` resets the test database. It persists across
  restarts otherwise, and leftovers cause quota, `409` and stale-seed failures.
- Do not stack `make dev-test`. `make kill` frees the ports but leaves the parent
  `go run ./cmd/devloop` processes, which then fight over `:4000` and hang requests for
  ~30s. Stop the old devloop before starting a new one.
- Restore the developer's normal `make dev` afterwards.

Local-only tests (excluded from CI) are gated with
`test.skip(!!process.env.CI && !process.env.E2E_LOCAL, "local-only")`, so
`CI=true E2E_LOCAL=1` still runs them against a side-car (see
`discovery-scan-method.spec.ts`).

## Writing tests that survive the full suite

Per-spec runs execute one file. CI runs everything in one order. These broke only
in the full suite:

- **A seeded row on an unfiltered, paged list.** The suite creates users, orgs and
  checks as it runs, so the oldest seeded row falls off page 1. Reach the row through
  the page's search or filter, with a non-matching query as the negative control.
- **Weak text assertions.** `toContainText("test")` passes on the wrong badge when the
  user belongs to `test`, `test2` and `test3`. Assert the exact text.
- **Ambiguous locators.** A card title (`<h3>`) can duplicate the page `<h1>` once data
  accumulates. Pin `getByRole("heading", { name, level: 1 })`.
- **Shared React Query keys.** Two screens reading the same key share its 60 s
  `staleTime`, so a list seeded by one screen hides a just-created entity from another.
  Suspect this whenever two features touch the same entity list.
- **Recharts dots.** `.recharts-wrapper circle` also matches the hover `activeDot`.
  Count data dots with `circle:has(title)`. A fractional expected count is the tell
  that a selector catches a transient element.
- **Hardcoded `http://localhost:4000`.** A spec that does not derive its API base from
  `E2E_BASE_URL` (via `e2e/base-url.ts`) silently hits the developer's devloop when run
  against a side-car.
