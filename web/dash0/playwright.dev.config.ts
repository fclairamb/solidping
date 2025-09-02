import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright configuration for E2E testing against running dev server.
 *
 * This simplified configuration:
 * - Assumes the backend server is already running (make dev-backend)
 * - Assumes the frontend dev server is running (bun run dev)
 * - No global setup/teardown (server already running)
 * - Tests against http://localhost:5174/dash0/ (Vite dev server)
 *
 * Usage:
 *   1. Start backend: make dev-backend (in project root)
 *   2. Start frontend: bun run dev (in web/dash0)
 *   3. Run tests: bun run test:e2e:dev
 *
 * See https://playwright.dev/docs/test-configuration.
 */
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: undefined,
  reporter: [["list"], ["html"]],

  use: {
    // Base URL for the dev server
    baseURL: "http://localhost:5174/dash0/",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
