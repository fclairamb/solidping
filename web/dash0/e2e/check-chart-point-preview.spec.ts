import { test, expect, mockSloCoverage } from "./fixtures";

/**
 * Tests for the chart dot preview (PinnedResultBox) behaviour:
 * - First click on a dot opens the preview box.
 * - Second click on the same dot dismisses the preview (does NOT navigate).
 * - Clicking the "More details →" link in the preview navigates to the result page.
 */

test.describe("Chart point preview", () => {
  // Helper: navigate to a check detail page with mocked results data so the
  // chart renders at least one dot we can interact with.
  async function gotoCheckDetailWithResults(page: Parameters<Parameters<typeof test.extend>[0]["authenticatedPage"]>[0]) {
    // The check-detail header's SLO coverage chip fires an unconditional
    // GET /slos?checkUid=… (spec 2026-08-20-01). Stub it so the networkidle
    // wait below has nothing real left to settle.
    await mockSloCoverage(page);

    const now = Date.now();
    const resultUid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

    // Mock results — one "raw" result point so the chart renders a dot.
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: resultUid,
              durationMs: 42,
              status: "up",
              periodStart: new Date(now - 5 * 60_000).toISOString(),
              periodType: "raw",
            },
          ],
          pagination: { total: 1, size: 1 },
        }),
      });
    });

    // Mock the single-result detail endpoint called by PinnedResultBox.
    await page.route(`**/api/v1/orgs/*/checks/*/results/${resultUid}`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid: resultUid,
          durationMs: 42,
          status: "up",
          periodStart: new Date(now - 5 * 60_000).toISOString(),
          periodType: "raw",
        }),
      })
    );

    await page.route("**/api/v1/orgs/*/incidents*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
      })
    );

    // Create a check and navigate to its detail page.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    const checkName = `E2E Preview ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/preview-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    return resultUid;
  }

  test("second click on the same dot dismisses the preview", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoCheckDetailWithResults(page);

    // Wait for the chart to render.
    await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

    // Find a clickable dot in the chart SVG.
    const chartDots = page.locator(".recharts-wrapper circle");
    const dotCount = await chartDots.count();

    // Skip if no dots rendered (e.g. no data displayed in current range).
    if (dotCount === 0) {
      test.skip(true, "No chart dots rendered — cannot test preview behaviour");
      return;
    }

    const firstDot = chartDots.first();

    // First click: preview should appear. Hover first so recharts establishes
    // the active data point before the click (mirrors real mouse interaction;
    // a synthetic click with no prior pointer movement leaves the chart state
    // empty). The selection is now URL-driven (spec 2026-07-14-01 §3), so the
    // click also writes a graphSelected param.
    await firstDot.hover({ force: true });
    await firstDot.click({ force: true });
    const previewBox = page.getByTestId("pinned-result-box");
    await expect(previewBox).toBeVisible({ timeout: 5000 });
    await page.waitForURL(/graphSelected=/, { timeout: 5000 });

    // Second click on the same dot: preview dismisses (does NOT navigate to the
    // result detail page) and the graphSelected param is cleared.
    await firstDot.hover({ force: true });
    await firstDot.click({ force: true });
    await expect(previewBox).not.toBeVisible({ timeout: 5000 });
    expect(page.url()).not.toContain("/results/");
    expect(page.url()).not.toMatch(/graphSelected=/);
  });

  test("More details link navigates to the result detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const resultUid = await gotoCheckDetailWithResults(page);

    // Wait for the chart to render.
    await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

    const chartDots = page.locator(".recharts-wrapper circle");
    const dotCount = await chartDots.count();

    if (dotCount === 0) {
      test.skip(true, "No chart dots rendered — cannot test More details link");
      return;
    }

    // Click the dot to open the preview. Hover first so recharts has an active
    // data point at click time (mirrors real mouse interaction).
    const firstDot = chartDots.first();
    await firstDot.hover({ force: true });
    await firstDot.click({ force: true });
    const previewBox = page.getByTestId("pinned-result-box");
    await expect(previewBox).toBeVisible({ timeout: 5000 });

    // The "More details →" link should be visible in the preview.
    const moreDetailsLink = previewBox.getByRole("link", { name: /more details/i });
    await expect(moreDetailsLink).toBeVisible();

    // Click the link — it should navigate to the result detail page.
    await moreDetailsLink.click();
    await page.waitForURL(`**/results/${resultUid}`, { timeout: 10000 });
    expect(page.url()).toContain(`/results/${resultUid}`);
  });

  test("a failing point still renders and pins a dot above the 150-point density threshold (single-series)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();
    const downUid = "single-series-down-result";
    const pointCount = 180; // above the 150-point dots-density threshold
    const downIndex = 90;
    const intervalMs = 5 * 60_000;

    // Single-region (no `region` field) dense dataset — takes the
    // single-series chart path, not the multi-series one.
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      const points = [];
      for (let i = 0; i < pointCount; i++) {
        const ts = now - (pointCount - i) * intervalMs;
        const isDown = i === downIndex;
        points.push({
          uid: isDown ? downUid : `raw-${i}`,
          durationMs: isDown ? 5000 : 42 + (i % 3) * 5,
          status: isDown ? "down" : "up",
          periodStart: new Date(ts).toISOString(),
          periodType: "raw",
        });
      }
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: points,
          pagination: { total: points.length, size: points.length },
        }),
      });
    });

    await page.route(`**/api/v1/orgs/*/checks/*/results/${downUid}`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid: downUid,
          durationMs: 5000,
          status: "down",
          periodStart: new Date(now - (pointCount - downIndex) * intervalMs).toISOString(),
          periodType: "raw",
        }),
      })
    );

    await page.route("**/api/v1/orgs/*/incidents*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
      })
    );

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    const checkName = `E2E Single Dense Down ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/single-dense-down-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

    // The gradient carries a red (COLOR_DOWN) stop despite the density gate
    // hiding neutral per-point dots.
    const COLOR_DOWN = "hsl(0, 72%, 51%)";
    const stopColors = await page
      .locator('linearGradient[id^="strokeGradient-"] stop')
      .evaluateAll((els) => els.map((el) => el.getAttribute("stop-color")));
    expect(stopColors).toContain(COLOR_DOWN);

    // Exactly the one down point renders a persistent dot; the ~179 healthy
    // points render invisible <g/> at this density.
    const persistentDots = page.locator(".recharts-wrapper .recharts-area-dots circle");
    await expect(persistentDots).toHaveCount(1);
    await expect(persistentDots.first()).toHaveAttribute("fill", COLOR_DOWN);

    // It stays clickable/pinnable.
    await persistentDots.first().hover({ force: true });
    await persistentDots.first().click({ force: true });
    await expect(page.getByTestId("pinned-result-box")).toBeVisible({
      timeout: 5000,
    });
  });
});
