# CI E2E Tests

## Overview

End-to-end tests run in CI as a dedicated GitHub Actions job (`e2e-tests`) using Playwright against the built solidping binary.

## Architecture

```
build job (produces solidping binary)
    └── e2e-tests job
            1. Download binary artifact
            2. Start solidping server (test mode, sqlite-memory)
            3. Run Playwright tests (chromium)
            4. Upload test artifacts
```

The `e2e-tests` job depends on the `build` job and gates the `ci` aggregation check, blocking PR merges if e2e tests fail.

## CI Environment

- **Runner**: ubuntu-24.04
- **Browser**: Chromium only (via Playwright)
- **Database**: SQLite in-memory (default when `SP_RUNMODE=test` and no `SP_DB_TYPE` is set)
- **Server mode**: `SP_RUNMODE=test` with `SP_DB_RESET=true` for deterministic test data
- **Test credentials**: `test@test.com` / `test` / org `test`

## Test Infrastructure

### Playwright Configuration (`web/dash0/playwright.config.ts`)

CI-specific behavior:
- `forbidOnly: true` — fails if `test.only` is left in code
- `retries: 2` — retries flaky tests up to 2 times
- `workers: 1` — serial execution to avoid resource conflicts
- Screenshots and video captured on failure
- Traces captured on first retry

### Global Setup (`web/dash0/e2e/global-setup.ts`)

Detects `CI=true` and skips build/startup (already handled by the workflow). Only waits for the server health endpoint (`/api/mgmt/health`) to respond.

### Global Teardown (`web/dash0/e2e/global-teardown.ts`)

Skips cleanup in CI — the workflow handles stopping services.

## Test Files

| File | What it tests |
|------|---------------|
| `e2e/login.spec.ts` | Authentication flow, error handling, logout |
| `e2e/dashboard.spec.ts` | Dashboard display, sidebar, layout |
| `e2e/checks.spec.ts` | Check management |
| `e2e/incidents.spec.ts` | Incident viewing |
| `e2e/fixtures.ts` | Shared `authenticatedPage` fixture (auto-login) |

## Artifacts

On every run (pass or fail), the workflow uploads:
- `web/dash0/playwright-report/` — HTML report
- `web/dash0/test-results/` — JSON results, screenshots, videos, traces

Retained for 7 days.

## Running Locally

```bash
# Against production build (builds everything, starts services)
cd web/dash0 && bun run test:e2e

# Against running dev server (no build, no service management)
cd web/dash0 && bun run test:e2e:dev

# With UI
cd web/dash0 && bun run test:e2e:ui
```
