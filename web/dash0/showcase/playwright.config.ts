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
    //
    // The viewport stays 1280x800 CSS px — that is the layout everything is
    // composed for — but the pixels behind it are captured at 2x. The zoom is
    // applied in post-production (see postprocess.ts), so without the extra
    // density every push-in would be an upscale of 1x pixels, i.e. blur. At
    // deviceScaleFactor 2 a zoom of up to 2x is pixel-exact; the choreography
    // stays at or below 1.8x to keep a margin.
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2,

    // Consistent theme across every capture, regardless of the host machine.
    colorScheme: "light",

    // Pacing is deliberate, not global. slowMo delays EVERY Playwright input
    // step, which turns the eased cursor travel in fixtures.ts (a burst of
    // mouse.move calls) into a crawl and makes the timing of the whole
    // choreography depend on how many steps a helper happens to use. The
    // recording asks for its beats explicitly instead — beat(), holdMs, the
    // travel duration — and this stays as an escape hatch, defaulting to off.
    launchOptions: {
      slowMo: Number(process.env.SHOWCASE_SLOW_MO ?? 0),
    },

    // Always record — that is the whole point of this project. Recorded at 2x
    // the published size so the post-production zoom has real pixels to crop
    // into; postprocess.ts scales the finished cut back down to 1280x800.
    video: {
      mode: "on",
      size: { width: 2560, height: 1600 },
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
