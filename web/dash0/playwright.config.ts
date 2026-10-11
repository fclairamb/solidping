import { defineConfig, devices } from "@playwright/test";
import { e2eBaseUrl, e2eBaseUrls } from "./e2e/base-url";

/**
 * Playwright configuration for E2E testing against production build.
 *
 * This configuration:
 * - Builds the complete server (frontend + backend with embedded resources)
 * - Starts PostgreSQL via docker-compose
 * - Runs the server with SOLIDPING_RUN_MODE=test for predictable test data
 * - Tests against http://localhost:4000/d/ (production build)
 * - Uses admin@solidping.io/solidpass credentials (default test mode)
 *
 * See https://playwright.dev/docs/test-configuration.
 */
export default defineConfig({
  testDir: "./e2e",
  // Run tests in files in parallel
  fullyParallel: true,
  // Fail the build on CI if you accidentally left test.only in the source code
  forbidOnly: !!process.env.CI,
  // Retry on CI only
  retries: process.env.CI ? 2 : 0,
  // On CI, one worker per target server (E2E_BASE_URLS, see e2e/base-url.ts):
  // every worker gets its own server and database. Workers never share a
  // server, because the suite mutates the shared `test` org.
  workers: process.env.CI ? e2eBaseUrls().length : undefined,
  // Reporter to use. On GitHub Actions the suite runs sharded (ci.yml), so
  // each shard writes a blob report and the e2e-report job merges them into
  // the HTML + JSON reports. Keyed on GITHUB_ACTIONS rather than CI so a local
  // `CI=true` run against a side-car server keeps its HTML report.
  reporter: process.env.GITHUB_ACTIONS
    ? [["blob"], ["list"]]
    : [
        ["html"],
        ["list"],
        ["json", { outputFile: "test-results/results.json" }],
      ],

  // Global setup and teardown
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",

  use: {
    // Base URL to use in actions like `await page.goto('/')`
    // This is the production server with embedded frontend. Overridable via
    // E2E_BASE_URL so a side-car test server (e.g. on another port) can be
    // targeted locally without disturbing a dev server on :4000.
    // E2E_BASE_URLS gives each worker its own server (e2e/base-url.ts).
    baseURL: e2eBaseUrl(),
    // Collect trace when retrying the failed test
    trace: "on-first-retry",
    // Take screenshot on failure
    screenshot: "only-on-failure",
    // Video on failure. "retain-on-failure" records every test and deletes
    // the file when it passes, so on CI (where a failure is always retried)
    // record the first retry only, like the trace.
    video: process.env.CI ? "on-first-retry" : "retain-on-failure",
  },

  // Configure projects for major browsers
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },

    // Run on Chromium only for faster testing
    // Uncomment to test on other browsers:
    // {
    //   name: "firefox",
    //   use: { ...devices["Desktop Firefox"] },
    // },
    // {
    //   name: "webkit",
    //   use: { ...devices["Desktop Safari"] },
    // },
  ],
});
