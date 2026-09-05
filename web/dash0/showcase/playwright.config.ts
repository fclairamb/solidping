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
    // deviceScaleFactor 2 buys genuinely 2x STILLS (page.screenshot() renders
    // at the device scale, so the raw PNGs are 2560x1600). It does NOT buy a 2x
    // VIDEO, however much one would like it to — see the note on `video` below.
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

    // Always record — that is the whole point of this project.
    //
    // The size MUST match the viewport in CSS px. Playwright records Chromium
    // by encoding CDP screencast frames, and those come back at the CSS-pixel
    // size of the viewport no matter what deviceScaleFactor says: measured
    // here, with deviceScaleFactor 2 the screenshots are 2560x1600 while the
    // screencast frames stay 1280x800. Asking for a larger video does not
    // upscale them — Playwright pastes each frame into the top-left corner of
    // the requested canvas and leaves the rest flat grey, which silently ruins
    // the take. (`Emulation.setDeviceMetricsOverride` with a `scale` of 2,
    // issued over a raw CDP session, does not change it either.)
    //
    // The practical consequence, and it is a real one: the post-production
    // zoom in postprocess.ts is an UPSCALE of 1x pixels, so a push-in trades
    // some sharpness for the framing. That is why the choreography stays
    // modest (<= 1.6x) and why the scale-down uses lanczos. Getting truly
    // crisp zooms would mean recording a 2560x1600 CSS viewport with the app
    // scaled up, i.e. filming a layout nobody ships — deliberately not done.
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
