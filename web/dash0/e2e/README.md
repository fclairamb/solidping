# E2E Testing with Playwright

This directory contains end-to-end tests for the SolidPing dashboard using Playwright.

## Quick Start

### Prerequisites

1. Install dependencies:
   ```bash
   bun install
   ```

2. Install Playwright browsers:
   ```bash
   bunx playwright install chromium
   ```

## Running Tests

### Against Development Server (Recommended for Development)

This mode assumes you have both backend and frontend servers already running.

1. Start the backend (from project root):
   ```bash
   make dev-backend
   ```

2. Start the frontend dev server (in web/dash0):
   ```bash
   bun run dev
   ```

3. Run tests:
   ```bash
   bun run test:e2e:dev
   ```

### Against Production Build

This mode automatically builds everything, starts PostgreSQL, and runs the server.

```bash
bun run test:e2e
```

## Available Test Commands

| Command | Description |
|---------|-------------|
| `bun run test:e2e` | Run tests against production build (builds, starts server, runs tests) |
| `bun run test:e2e:dev` | Run tests against running dev server |
| `bun run test:e2e:ui` | Open Playwright UI mode (visual test runner) |
| `bun run test:e2e:headed` | Run tests with browser visible |
| `bun run test:e2e:debug` | Run tests in debug mode (step through) |
| `bun run test:report` | Open the HTML test report |

## Test Structure

```
e2e/
├── fixtures.ts        # Custom test fixtures (e.g., authenticatedPage)
├── global-setup.ts    # Runs before all tests (builds, starts server)
├── global-teardown.ts # Runs after all tests (stops server)
├── login.spec.ts      # Login flow tests
├── dashboard.spec.ts  # Dashboard tests (authenticated)
└── README.md          # This file
```

## Writing Tests

### Using Test IDs

Tests should use `data-testid` attributes for reliable element selection:

```typescript
// In component
<Button data-testid="login-submit">Sign in</Button>

// In test
await page.getByTestId("login-submit").click();
```

### Using the Authenticated Page Fixture

For tests that require authentication, use the `authenticatedPage` fixture:

```typescript
import { test, expect } from "./fixtures";

test("should see dashboard", async ({ authenticatedPage }) => {
  await authenticatedPage.goto("/");
  // authenticatedPage is already logged in
});
```

### Test ID Conventions

- Login form: `login-logo`, `login-title`, `login-email`, `login-password`, `login-submit`, `login-error`
- Sidebar: `app-sidebar`, `sidebar-trigger`

## Configuration

- `playwright.config.ts` - Production build configuration (with global setup/teardown)
- `playwright.dev.config.ts` - Dev server configuration (no setup/teardown)

## Test Credentials

Default test credentials (from CLAUDE.md):
- Email: `admin@solidping.com`
- Password: `solidpass`
- Organization: `default`
