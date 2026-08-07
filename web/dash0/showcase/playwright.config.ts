import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright configuration for the **showcase media pipeline**.
 *
 * This is deliberately NOT a test suite:
 *
 * - It lives outside `web/dash0/e2e/`, and the e2e config
 *   (`web/dash0/playwright.config.ts`) pins `testDir: "./e2e"`, so
 *   `bunx playwright test` — and therefore CI — never picks these files up.
 * - Its files are named `*.showcase.ts`, which does not match Playwright's
 *   default `testMatch` either, as a second line of defence.
 *
 * It exists to drive the real dash0 UI and record polished screenshots and
 * screen recordings for the docs "Tour" page. Run it with:
 *
 *   make showcase
 *
 * Requires a running SolidPing server. Point it somewhere other than the
 * default with the same `E2E_BASE_URL` convention the e2e suite uses, e.g.
 *
 *   E2E_BASE_URL=http://localhost:4321/dash0/ make showcase
 */
export default defineConfig({
  testDir: "./specs",
  testMatch: "**/*.showcase.ts",

  // Media capture is inherently sequential and must never be retried: a retry
  // would leave two conflicting videos behind.
  fullyParallel: false,
  workers: 1,
  retries: 0,

  // A recording that hangs should fail loudly rather than block `make showcase`.
  timeout: 180_000,

  reporter: [["list"]],

  // Raw Playwright artifacts (videos, traces) land here. Git-ignored — only
  // the post-processed assets under web/docs/static/showcase/ are committed.
  outputDir: "./output/run",

  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:4000/dash0/",

    ...devices["Desktop Chrome"],

    // Fixed, predictable frame for every asset.
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 1,

    // Consistent theme across every capture, regardless of the host machine.
    colorScheme: "light",

    // Human pacing: every action is slowed down so a viewer can follow along.
    launchOptions: {
      slowMo: Number(process.env.SHOWCASE_SLOW_MO ?? 250),
    },

    // Always record — that is the whole point of this project.
    video: {
      mode: "on",
      size: { width: 1280, height: 800 },
    },
    trace: "off",
    screenshot: "off",
  },

  projects: [
    {
      name: "showcase",
    },
  ],
});
