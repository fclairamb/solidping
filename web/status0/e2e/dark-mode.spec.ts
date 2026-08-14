import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * Covers spec 2026-08-14-05: the public status page previously had no dark
 * mode at all (`.dark` was dead CSS — nothing ever applied the class). This
 * verifies the visitor-facing toggle (mirrors dash0's e2e/dark-mode.spec.ts)
 * and, separately, the no-flash default that dash0 does NOT need (dash0
 * applies the class in a useEffect, which is fine behind a login; this is a
 * public page, so a dark-mode visitor must never see a white first paint).
 *
 * Uses a mocked status-page API response (same technique as
 * overall-status-badge.spec.ts) so the page renders deterministically
 * without depending on dev-seed data.
 */
const ORG = "e2e-dark-mode";
const SLUG = "dark-mode";

function basePayload() {
  return {
    uid: "22222222-2222-2222-2222-222222222222",
    name: "Dark Mode Test",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 0,
    historyPeriod: "24h",
    overallStatus: "operational",
    sections: [],
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
  };
}

async function mockStatusPage(page: Page) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(basePayload()),
    }),
  );
}

// Perceived luminance from a "rgb(r, g, b)" / "rgba(r, g, b, a)" computed
// style string. >0.5 reads as a light surface, <0.5 as dark — good enough to
// tell the two palettes apart without hardcoding an exact color that would
// break the moment index.css's oklch values are retuned.
function luminance(rgbString: string): number {
  const parts = rgbString.match(/[\d.]+/g)?.map(Number) ?? [255, 255, 255];
  const [r, g, b] = parts;
  return (0.299 * r + 0.587 * g + 0.114 * b) / 255;
}

test.describe("Public status page — dark mode", () => {
  test("toggle flips html.dark, persists to localStorage, and survives reload", async ({
    page,
  }) => {
    await mockStatusPage(page);
    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const toggle = page.getByTestId("theme-toggle");
    await expect(toggle).toBeVisible();

    // Starting state depends on the test runner's OS color scheme, so read
    // it rather than assuming light.
    const initiallyDark = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );

    await toggle.click();
    const afterFirstClick = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(afterFirstClick).toBe(!initiallyDark);

    const storedTheme = await page.evaluate(() => localStorage.getItem("theme"));
    expect(storedTheme).toBe(afterFirstClick ? "dark" : "light");

    await page.reload();
    await page.waitForLoadState("domcontentloaded");
    const afterReload = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(afterReload).toBe(afterFirstClick);

    // Toggle back — round-trips through all three effective states (stored
    // light, stored dark, follow-system) over the course of this test.
    await toggle.click();
    const afterSecondClick = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(afterSecondClick).toBe(initiallyDark);
  });

  test("prefers-color-scheme: light with no stored preference stays light", async ({
    page,
  }) => {
    await mockStatusPage(page);
    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`, {
      waitUntil: "domcontentloaded",
    });

    const hasDarkClass = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(hasDarkClass).toBe(false);
  });
});

test.describe("Public status page — dark mode (colorScheme: dark)", () => {
  test.use({ colorScheme: "dark" });

  test("no stored preference: html.dark is set before networkidle", async ({
    page,
  }) => {
    await mockStatusPage(page);
    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`, {
      waitUntil: "domcontentloaded",
    });

    const hasDarkClass = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(hasDarkClass).toBe(true);
  });

  test("no stored preference yields a dark first paint, before the bundle ever loads (no white flash)", async ({
    page,
    context,
  }) => {
    // Block every script request — React never mounts. Anything that makes
    // the page look dark under this condition can only come from the inline
    // script in index.html plus static CSS, which is exactly the no-flash
    // guarantee under test: a stored/OS preference of dark must never render
    // light even for the instant before hydration.
    await context.route("**/*", (route) => {
      if (route.request().resourceType() === "script") {
        return route.abort();
      }
      return route.continue();
    });

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`, {
      waitUntil: "domcontentloaded",
    });

    const hasDarkClass = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(hasDarkClass).toBe(true);

    const bodyBg = await page.evaluate(
      () => getComputedStyle(document.body).backgroundColor,
    );
    expect(luminance(bodyBg)).toBeLessThan(0.5);
  });

  test("stored light preference overrides prefers-color-scheme: dark", async ({
    page,
  }) => {
    await mockStatusPage(page);
    // Seed localStorage before the app's own script runs, via an init
    // script that Playwright guarantees executes before any page script.
    await page.addInitScript(() => {
      localStorage.setItem("theme", "light");
    });
    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`, {
      waitUntil: "domcontentloaded",
    });

    const hasDarkClass = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    expect(hasDarkClass).toBe(false);
  });
});
