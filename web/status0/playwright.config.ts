import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright config for status0 public status page tests.
 * Assumes the SolidPing server is already running at http://localhost:4000.
 * Tests are public-facing (no authentication required).
 */
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: [["list"], ["html"]],
  use: {
    baseURL: "http://localhost:4000",
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
