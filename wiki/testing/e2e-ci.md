# CI E2E Tests

## Overview

End-to-end tests run in CI as the `e2e-tests` GitHub Actions job, using Playwright
against the built solidping binary. The dash0 suite is split into **2 shards** that
run in parallel, each on its own runner. Each shard runs 2 Playwright workers, and
each worker has its own server and database.

## Architecture

```
build job (produces solidping binary)
    └── e2e-tests (matrix: shard 1, shard 2), each:
            1. Download binary artifact
            2. Start Postgres + 2 solidping servers (:4000, :4010), each on its own fresh DB
            3. playwright test --shard=N/2 (chromium, 2 workers, one per server)
            4. shard 1 only: status0 suite
               shard 2 only: session-continuity against a short-expiry side-car
            5. Upload blob report + failure artifacts
    └── e2e-report
            merge-reports → HTML report + results.json, slowest files in the step summary
```

The `ci` gate depends on `e2e-tests` (both shards). `e2e-report` is reporting only.

## Why 2 shards, 2 workers each, one server per worker

- **Nothing is shared between workers**: every spec shares the seeded `test` org,
  and several mutate it (integrations, system parameters, status-page domains), so
  two workers against ONE server would interfere. Each worker therefore gets its
  own server and database: `E2E_BASE_URLS` lists the servers and
  `web/dash0/e2e/base-url.ts` maps a worker's `TEST_PARALLEL_INDEX` to one of them
  (`baseURL`, `API_BASE` and the global-setup health wait all go through it).
- **2 is a cap on parallel runners**, chosen on purpose. Each runner has 4 vCPUs,
  which comfortably hosts 2 servers + 2 browsers + Postgres.
- A spec that passes alone but fails here usually depended on state an earlier
  spec left behind in the same database: fix the spec, it is order-dependent.

### Balancing

Playwright splits by test **count** into contiguous halves (alphabetical file order),
not by duration. status0 (~1.5 min) runs on shard 1 and the session-continuity + SSO
side-car (~3 min) on shard 2, and `PWTEST_SHARD_WEIGHTS: "53:47"` (read by Playwright
1.61, undocumented) gives shard 1 more tests to make up for it. Recompute it from the
merged `results.json` when the two shards drift apart. Re-check the split in the
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
- `workers`: one per entry of `E2E_BASE_URLS` (1 without it)
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

# One shard, the way CI runs it: one server (own DB) per worker
cd web/dash0 && CI=true E2E_BASE_URLS=http://localhost:4300/d/,http://localhost:4310/d/ \
  bunx playwright test --shard=1/2

# Against running dev server (no build, no service management)
cd web/dash0 && bun run test:e2e:dev
```
