import { test, expect, mockSloCoverage } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * Tests for drag-to-select X (time) zoom on the response-time chart
 * (spec 2026-07-14-01):
 * - Dragging a horizontal range writes graphFrom/graphTo to the URL and shows
 *   a "Reset zoom" control.
 * - The zoomed fetch requests only the selected window (periodEndBefore is sent
 *   to the results endpoint — the server-side narrowing, not a client re-scale).
 * - Reset clears graphFrom/graphTo (and graphSelected) from the URL.
 * - A cold deep-link with graphFrom/graphTo reconstructs the zoom (Reset control
 *   present, narrowed fetch issued).
 */

const RESULT_UID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

// A day's worth of dense raw points so the chart renders a wide, draggable line.
function buildResults() {
  const now = Date.now();
  const points = [];
  const count = 120;
  const intervalMs = 12 * 60_000; // 12 min → ~24h span
  for (let i = 0; i < count; i++) {
    points.push({
      uid: i === 60 ? RESULT_UID : `raw-${i}`,
      durationMs: 40 + (i % 5) * 8,
      status: "up",
      periodStart: new Date(now - (count - i) * intervalMs).toISOString(),
      periodType: "raw",
    });
  }
  return points;
}

// Navigate to a freshly-created check's detail page with mocked results, and
// return the list of results-request URLs captured so far (mutated live).
async function gotoCheckDetail(page: Page): Promise<string[]> {
  // The check-detail header's SLO coverage chip fires an unconditional
  // GET /slos?checkUid=… (spec 2026-08-20-01). Stub it so the networkidle
  // wait below has nothing real left to settle.
  await mockSloCoverage(page);

  const resultsUrls: string[] = [];
  const points = buildResults();

  await page.route("**/api/v1/orgs/*/results*", (route) => {
    resultsUrls.push(route.request().url());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: points,
        pagination: { total: points.length, size: points.length },
      }),
    });
  });

  await page.route(`**/api/v1/orgs/*/checks/*/results/${RESULT_UID}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        uid: RESULT_UID,
        durationMs: 42,
        status: "up",
        periodStart: new Date().toISOString(),
        periodType: "raw",
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/incidents*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
    }),
  );

  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");

  await page.getByTestId("check-name-input").fill(`E2E Zoom ${Date.now()}`);
  await page.getByTestId("check-url-input").fill("https://example.com/zoom-test");
  await page.getByTestId("check-submit-button").click();

  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
  await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

  return resultsUrls;
}

// Drag horizontally across the middle of the chart to select a sub-window.
async function dragZoom(page: Page) {
  const surface = page.locator(".recharts-wrapper").first();
  const box = await surface.boundingBox();
  if (!box) throw new Error("chart has no bounding box");
  const y = box.y + box.height / 2;
  const startX = box.x + box.width * 0.3;
  const endX = box.x + box.width * 0.7;

  await page.mouse.move(startX, y);
  await page.mouse.down();
  // Several intermediate moves so recharts resolves an active x label.
  for (let i = 1; i <= 6; i++) {
    await page.mouse.move(startX + ((endX - startX) * i) / 6, y);
  }
  await page.mouse.up();
}

test.describe("Response-time chart zoom", () => {
  test("drag selects a window, writes graphFrom/graphTo, and can be reset", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const resultsUrls = await gotoCheckDetail(page);
    const beforeCount = resultsUrls.length;

    await dragZoom(page);

    // URL now carries the zoom window.
    await page.waitForURL(/graphFrom=/, { timeout: 5000 });
    expect(page.url()).toMatch(/graphFrom=\d+/);
    expect(page.url()).toMatch(/graphTo=\d+/);

    // A Reset control appears while zoomed.
    const resetBtn = page.getByTestId("response-time-chart-reset-zoom");
    await expect(resetBtn).toBeVisible();

    // The zoomed fetch narrows the window server-side via periodEndBefore.
    await expect
      .poll(() => resultsUrls.slice(beforeCount).some((u) => u.includes("periodEndBefore")), {
        timeout: 5000,
      })
      .toBe(true);

    // Reset clears the window from the URL and hides the control.
    await resetBtn.click();
    await expect(resetBtn).not.toBeVisible({ timeout: 5000 });
    expect(page.url()).not.toMatch(/graphFrom=/);
    expect(page.url()).not.toMatch(/graphTo=/);
  });

  test("cold deep-link with graphFrom/graphTo reconstructs the zoom", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const resultsUrls = await gotoCheckDetail(page);

    // Deep-link the current check URL with an explicit zoom window (last 3h).
    const to = Date.now();
    const from = to - 3 * 3_600_000;
    const base = page.url().split("?")[0];
    const beforeCount = resultsUrls.length;
    await page.goto(`${base}?graphFrom=${from}&graphTo=${to}`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

    // Reset control present on a cold load (zoom reconstructed from the URL).
    await expect(page.getByTestId("response-time-chart-reset-zoom")).toBeVisible({
      timeout: 5000,
    });

    // The narrowed fetch was issued for the deep-linked window.
    await expect
      .poll(() => resultsUrls.slice(beforeCount).some((u) => u.includes("periodEndBefore")), {
        timeout: 5000,
      })
      .toBe(true);
  });
});
