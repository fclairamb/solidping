import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright configuration for E2E testing against production build.
 *
 * This configuration:
 * - Builds the complete server (frontend + backend with embedded resources)
 * - Starts PostgreSQL via docker-compose
 * - Runs the server with SOLIDPING_RUN_MODE=test for predictable test data
 * - Tests against http://localhost:4000/dash0/ (production build)
 * - Uses admin@solidping.com/solidpass credentials (default test mode)
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
  // Opt out of parallel tests on CI
  workers: process.env.CI ? 1 : undefined,
  // Reporter to use
  reporter: [
    ["html"],
    ["list"],
    ["json", { outputFile: "test-results/results.json" }],
  ],

  // Global setup and teardown
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",

  use: {
    // Base URL to use in actions like `await page.goto('/')`
    // This is the production server with embedded frontend
    baseURL: "http://localhost:4000/dash0/",
    // Collect trace when retrying the failed test
    trace: "on-first-retry",
    // Take screenshot on failure
    screenshot: "only-on-failure",
    // Video on failure
    video: "retain-on-failure",
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
