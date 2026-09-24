# CI E2E Tests

## Overview

End-to-end tests run in CI as the `e2e-tests` GitHub Actions job, using Playwright
against the built solidping binary. The dash0 suite is split into **2 shards** that
run in parallel, each on its own runner with its own Postgres and server.

## Architecture

```
build job (produces solidping binary)
    └── e2e-tests (matrix: shard 1, shard 2), each:
            1. Download binary artifact
            2. Start Postgres + solidping server (test mode, fresh DB)
            3. playwright test --shard=N/2 (chromium, 1 worker)
            4. shard 1 only: status0 suite
               shard 2 only: session-continuity against a short-expiry side-car
            5. Upload blob report + failure artifacts
    └── e2e-report
            merge-reports → HTML report + results.json, slowest files in the step summary
```

The `ci` gate depends on `e2e-tests` (both shards). `e2e-report` is reporting only.

## Why 2 shards, 1 worker each

- **Shards share nothing**: each has its own database, so tests in one shard cannot
  see state written by the other.
- **Inside a shard, 1 worker**: every spec shares the seeded `test` org, and several
  mutate it (integrations, system parameters, status-page domains). Running two
  workers against one server is not safe until tests are isolated per worker.
- **2 is a cap on parallel runners**, chosen on purpose. Raising it is a one-line
  matrix change, plus re-balancing (below).

### Balancing

Playwright splits by test **count** into contiguous halves (alphabetical file order),
not by duration. As of 2026-09-24 the first half was ~1.5 min heavier, so the two
extra suites are placed to even it out: status0 (~1.5 min) on shard 1, the
session-continuity side-car (~3 min) on shard 2. Re-check the split in the
`E2E Report` step summary when a shard starts finishing much later than the other.

## CI Environment

- **Runner**: ubuntu-26.04
- **Browser**: Chromium only
- **Database**: Postgres from `docker-compose.yml` (`SP_DB_TYPE=postgres`), reset at start
- **Server env**: `SP_RUNMODE=test`, rate limiting off, `SP_AUTH_REGISTRATION_EMAIL_PATTERN=.*`
  (a local side-car must match these, see the `/implement-todos` side-car recipe)
- **Test credentials**: `test@test.com` / `test` / org `test`

## Playwright Configuration (`web/dash0/playwright.config.ts`)

CI-specific behavior:
- `forbidOnly: true`
- `retries: 2`
- `workers: 1`
- Trace and video on the first retry only; screenshot on failure
- On GitHub Actions (`GITHUB_ACTIONS`), reporters are `blob` + `list`. A local
  `CI=true` run keeps the HTML/JSON reporters.

## Artifacts

- `playwright-report`: the merged HTML report, with `results.json` (every test's
  duration) inside. 7 days.
- `e2e-artifacts-1`, `e2e-artifacts-2`: failure screenshots, traces, videos, the
  status0 report and the server logs of each shard. 7 days.
- `blob-report-1`, `blob-report-2`: raw shard reports, input of the merge. 1 day.

To open a merged report locally: download `playwright-report`, then
`bunx playwright show-report <dir>`.

## Concurrency

A new push to a pull request cancels that PR's in-flight CI run. Pushes to `main`
and tag builds are never cancelled.

## Running Locally

```bash
# Against production build (builds everything, starts services)
cd web/dash0 && bun run test:e2e

# One shard, the way CI runs it
cd web/dash0 && CI=true E2E_BASE_URL=http://localhost:4300/d/ bunx playwright test --shard=1/2

# Against running dev server (no build, no service management)
cd web/dash0 && bun run test:e2e:dev
```
