import { test, expect, mockSloCoverage } from "./fixtures";

// Every test in this file lands on the check detail page and immediately waits
// on networkidle, and several mock the rest of that page's traffic to stay
// hermetic. The header's SLO coverage chip fires an unconditional
// GET /slos?checkUid=… (spec 2026-08-20-01), so stub it here once rather than
// leaving one real request for each of those waits to sit through.
test.beforeEach(async ({ authenticatedPage }) => {
  await mockSloCoverage(authenticatedPage);
});

test.describe("Check Detail Page", () => {
  test("should not make excessive API requests", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check so we have a detail page to visit
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Query Count ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/query-test");
    await page.getByTestId("check-submit-button").click();

    // Wait for check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Now count API requests over 5 seconds
    const apiRequests: string[] = [];
    page.on("request", (request) => {
      const url = request.url();
      if (url.includes("/api/")) {
        apiRequests.push(url);
      }
    });

    // Wait 5 seconds and count requests
    await page.waitForTimeout(5000);

    // With the check detail page components (summary cards, chart, availability table,
    // recent results, incidents), we expect a bounded number of API requests.
    // The initial load makes ~8-10 requests (check, results x4, incidents x2, etc.).
    // Periodic refetches may add a few more. But it should never be hundreds.
    const requestCount = apiRequests.length;

    // Take screenshot for debugging
    await page.screenshot({
      path: "test-results/screenshots/check-detail-query-count.png",
      fullPage: true,
    });

    // Assert: should be well under 50 requests in 5 seconds
    // A query storm would produce hundreds or thousands
    expect(
      requestCount,
      `Expected fewer than 50 API requests in 5s, got ${requestCount}. URLs: ${apiRequests.slice(0, 10).join("\n")}`
    ).toBeLessThan(50);
  });

  test("should display summary cards", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Create a check
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Summary ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/summary-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify the new dashboard sections are present
    await expect(page.getByText("Last checked")).toBeVisible();
    await expect(page.getByTestId("incidents-card")).toBeVisible();
    await expect(page.getByText("Response Times")).toBeVisible();
    await expect(page.getByText("Availability").first()).toBeVisible();

    // Verify time range buttons for chart
    await expect(page.getByRole("button", { name: "day" })).toBeVisible();
    await expect(page.getByRole("button", { name: "week" })).toBeVisible();
    await expect(page.getByRole("button", { name: "month" })).toBeVisible();

    // Verify availability table headers (may take longer as it depends on multiple API calls)
    await expect(page.getByText("Time period")).toBeVisible({ timeout: 15000 });
    await expect(page.getByText("Downtime")).toBeVisible();

    await page.screenshot({
      path: "test-results/screenshots/check-detail-dashboard.png",
      fullPage: true,
    });
  });

  test("Recent Results table shows the region a result ran from, not a dash", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check so a first result lands and populates Recent Results.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Region Column ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/region-column-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // CreateCheck synchronously inserts a "created" placeholder result row
    // (status: created) in the same transaction as the check itself, before
    // any worker ever claims the job. StatusBadge renders that raw status
    // string ("created") as its label, so it's a stable way to exclude the
    // placeholder. That row structurally has no region (it predates
    // execution), so grabbing `.first()` of all result rows can flakily land
    // on it instead of a real, executed result — wait for a row whose status
    // badge is NOT "created" instead. The real result needs job scheduling +
    // worker claim + an actual HTTP request to example.com + save + the
    // page's poll/refetch to land, so this needs the same order of headroom
    // as the 30s fast-poll window documented above, not the default
    // expect() timeout.
    const executedRow = page
      .locator('[data-testid^="result-row-"]')
      .filter({ hasNot: page.getByText("created", { exact: true }) })
      .first();
    await expect(executedRow).toBeVisible({ timeout: 30000 });

    // The Region cell must render the actual region the result ran from, not
    // the "-" placeholder used only for legacy rows with no stored region.
    // Single-worker local/e2e setups register under region "default", which
    // has no configured region-definition system parameter, so it renders
    // via the built-in fallback definition (📍 Default) as an outline Badge.
    const regionCell = executedRow.getByTestId("result-region-cell");
    await expect(regionCell).toBeVisible({ timeout: 30000 });
    await expect(regionCell).not.toHaveText("-");
    await expect(regionCell.getByText(/default/i)).toBeVisible();
  });

  test("Response Times chart: region chips filter to a single region's points, cross-filter Recent Results, carry a single ?region= param, and never remount the chart", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();

    // Mock a multi-region results response: a low-latency "us-1" cluster and
    // a high-latency "eu-1" cluster, interleaved in time — the exact shape
    // that renders as a cross-region sawtooth without the filter. The same
    // handler backs both the chart's window fetch and the Recent Results
    // list fetch (which additionally echoes back whichever `region=` it
    // received), and counts each distinct call style separately so the test
    // can prove the chart itself never re-fetches on a region change.
    let chartFetchCount = 0;
    let listFetchCount = 0;
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = new URL(route.request().url());
      if (!url.pathname.includes("/results")) return route.continue();
      const region = url.searchParams.get("region");
      // Recent Results requests `limit=10` (useResults); the chart's own
      // window fetch (useResultTiers — one request per aggregation tier, whose
      // react-query keys the page's chartWindowResults shares) always requests
      // `limit=1000` and never sends `region` at all — region filtering there
      // is client-side. This handler ignores `periodType`, so every tier gets
      // the same rows back; mergeResultTiers dedups them by uid, so the point
      // counts below are the same as they were before the tier split.
      const isListCall = url.searchParams.get("limit") === "10";
      if (isListCall) {
        listFetchCount++;
      } else {
        chartFetchCount++;
      }

      const chartPoints = [];
      for (let i = 0; i < 8; i++) {
        const ts = now - (16 - i * 2) * 60_000;
        chartPoints.push({
          uid: `us1-${i}`,
          durationMs: 55 + (i % 2) * 5,
          status: "up",
          region: "us-1",
          periodStart: new Date(ts).toISOString(),
          periodType: "raw",
        });
        chartPoints.push({
          uid: `eu1-${i}`,
          durationMs: 650 + (i % 2) * 10,
          status: "up",
          region: "eu-1",
          periodStart: new Date(ts + 30_000).toISOString(),
          periodType: "raw",
        });
      }
      // The Recent Results list (size=10) uses its own small fixed dataset —
      // one row per region — so its expected row counts below stay simple
      // regardless of the chart's denser point cloud. It additionally
      // narrows server-side by whichever region= param it received, matching
      // the real API's filtering behavior.
      const listPoints = [chartPoints[0], chartPoints[1]];
      const data = isListCall
        ? region
          ? listPoints.filter((p) => p.region === region)
          : listPoints
        : chartPoints;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data,
          pagination: { total: data.length, size: data.length },
        }),
      });
    });

    await page.route("**/api/v1/orgs/*/regions*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { slug: "us-1", emoji: "🇺🇸", name: "US East" },
            { slug: "eu-1", emoji: "🇪🇺", name: "EU West" },
          ],
          defaultRegions: ["us-1"],
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

    const checkName = `E2E Region Filter ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/region-filter-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Both region chips render (emoji + name), plus "All regions". "All" is
    // selected by default.
    const filterRow = page.getByTestId("response-time-chart-region-filter");
    await expect(filterRow).toBeVisible({ timeout: 15000 });
    const allChip = filterRow.getByRole("button", { name: "All regions" });
    const usChip = filterRow.getByRole("button", { name: /US East/ });
    const euChip = filterRow.getByRole("button", { name: /EU West/ });
    await expect(allChip).toBeVisible();
    await expect(usChip).toBeVisible();
    await expect(euChip).toBeVisible();

    // Recent Results starts with both rows (its own filter row is visible
    // too, since >1 region is observed).
    const resultsFilter = page.getByTestId("results-region-filter");
    await expect(resultsFilter).toBeVisible();
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(2);

    await expect(page.locator(".recharts-wrapper")).toBeVisible();
    // Count only the per-point data dots — each carries a <title> child. The
    // hover/selected activeDot is a childless <circle> whose nondeterministic
    // presence would otherwise make this total odd (17 vs 16) and the /2
    // assertions below fractional (e.g. expected 8.5). See response-time-chart.tsx.
    const dataDots = page.locator(".recharts-wrapper circle:has(title)");
    const allDotCount = await dataDots.count();

    // Mark the chart's own wrapper DOM node directly (bypassing React) so a
    // remount — which would create a brand-new node — is distinguishable
    // from an in-place re-render, which keeps this expando attribute intact.
    const chartWrapper = page.getByTestId("response-time-chart-wrapper");
    await chartWrapper.evaluate((el) => el.setAttribute("data-e2e-no-remount", "still-here"));
    await page.waitForLoadState("networkidle");
    const chartFetchCountBeforeRegionChange = chartFetchCount;

    // Selecting the US chip narrows the chart AND cross-filters Recent
    // Results (spec 2026-08-13-02), and writes a single ?region=us-1 param —
    // no separate graphRegion/resultsRegion keys.
    await usChip.click();
    await page.waitForURL(/[?&]region=us-1(&|$)/);
    expect(page.url()).not.toContain("graphRegion");
    expect(page.url()).not.toContain("resultsRegion");
    await expect(dataDots).toHaveCount(allDotCount / 2);
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(1);

    // No remount: the wrapper DOM node is the same one marked above, and the
    // chart's own results fetch (query key has no region in it — region
    // filtering is client-side) never re-fired, only the Recent Results list
    // call (which does carry region= server-side) did.
    await expect(chartWrapper).toHaveAttribute("data-e2e-no-remount", "still-here");
    expect(chartFetchCount).toBe(chartFetchCountBeforeRegionChange);
    expect(listFetchCount).toBeGreaterThan(0);

    // Switching to the EU chip updates the URL and keeps a single-region
    // point count (same size class as the US selection, different region),
    // still without remounting the chart.
    await euChip.click();
    await page.waitForURL(/[?&]region=eu-1(&|$)/);
    await expect(dataDots).toHaveCount(allDotCount / 2);
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(1);
    await expect(chartWrapper).toHaveAttribute("data-e2e-no-remount", "still-here");
    expect(chartFetchCount).toBe(chartFetchCountBeforeRegionChange);

    // Back to "All regions" restores every point (chart and Recent Results
    // alike) and clears the param.
    await allChip.click();
    await expect(dataDots).toHaveCount(allDotCount);
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(2);
    expect(page.url()).not.toContain("region=");
    await expect(chartWrapper).toHaveAttribute("data-e2e-no-remount", "still-here");
  });

  test("Response Times chart: single-region check shows no region chips", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();

    // Mock a single-region results response — the chip row must stay hidden,
    // matching today's behavior (no passive subtitle either).
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            {
              uid: "single-region-1",
              durationMs: 42,
              status: "up",
              region: "us-1",
              periodStart: new Date(now - 5 * 60_000).toISOString(),
              periodType: "raw",
            },
          ],
          pagination: { total: 1, size: 1 },
        }),
      });
    });

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

    const checkName = `E2E Single Region ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/single-region-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

    await expect(
      page.getByTestId("response-time-chart-region-filter"),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "All regions" }),
    ).toHaveCount(0);

    // Single-region checks also show no Recent Results filter chips, no
    // swatch legend, and no stats strip (spec 2026-07-05-13, criterion 2).
    await expect(page.getByTestId("results-region-filter")).toHaveCount(0);
    await expect(page.getByTestId("results-duration-stats")).toHaveCount(0);
    await expect(
      page.locator('[data-testid^="response-time-chart-region-swatch-"]'),
    ).toHaveCount(0);
  });

  test("Response Times chart: All-regions view renders one distinct-colored line per region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();

    // Mock a two-region results response — a near cluster and a far cluster,
    // interleaved in time, exactly the shape spec 2026-07-05-13 requires one
    // line per region for.
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      const points = [];
      for (let i = 0; i < 8; i++) {
        const ts = now - (16 - i * 2) * 60_000;
        points.push({
          uid: `us1-${i}`,
          durationMs: 55 + (i % 2) * 5,
          status: "up",
          region: "us-1",
          periodStart: new Date(ts).toISOString(),
          periodType: "raw",
        });
        points.push({
          uid: `eu1-${i}`,
          durationMs: 650 + (i % 2) * 10,
          status: "up",
          region: "eu-1",
          periodStart: new Date(ts + 30_000).toISOString(),
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

    await page.route("**/api/v1/orgs/*/regions*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { slug: "us-1", emoji: "🇺🇸", name: "US East" },
            { slug: "eu-1", emoji: "🇪🇺", name: "EU West" },
          ],
          defaultRegions: ["us-1"],
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

    const checkName = `E2E Multi Series ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/multi-series-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // "All regions" is selected by default: both chips carry a color swatch
    // (they double as the legend), the "All regions" chip does not.
    const filterRow = page.getByTestId("response-time-chart-region-filter");
    await expect(filterRow).toBeVisible({ timeout: 15000 });
    await expect(page.locator(".recharts-wrapper")).toBeVisible();

    const usSwatch = page.getByTestId("response-time-chart-region-swatch-us-1");
    const euSwatch = page.getByTestId("response-time-chart-region-swatch-eu-1");
    await expect(usSwatch).toBeVisible();
    await expect(euSwatch).toBeVisible();
    await expect(
      filterRow
        .getByRole("button", { name: "All regions" })
        .locator('[data-testid^="response-time-chart-region-swatch-"]'),
    ).toHaveCount(0);

    // The two chip swatches carry distinct background colors (--chart-1 vs
    // --chart-2), and the actual chart lines (recharts Area path elements)
    // render with two distinct, non-empty stroke colors — proving one visibly
    // distinct line per region rather than a single merged series.
    const usSwatchColor = await usSwatch.evaluate(
      (el) => getComputedStyle(el).backgroundColor,
    );
    const euSwatchColor = await euSwatch.evaluate(
      (el) => getComputedStyle(el).backgroundColor,
    );
    expect(usSwatchColor).toBeTruthy();
    expect(euSwatchColor).toBeTruthy();
    expect(usSwatchColor).not.toBe(euSwatchColor);

    const areaStrokes = await page
      .locator(".recharts-area-curve")
      .evaluateAll((els) => els.map((el) => el.getAttribute("stroke")));
    const distinctStrokes = new Set(areaStrokes.filter(Boolean));
    expect(areaStrokes.length).toBeGreaterThanOrEqual(2);
    expect(distinctStrokes.size).toBeGreaterThanOrEqual(2);

    // Hovering a dot shows the region (emoji+name) in the tooltip.
    const dots = page.locator(".recharts-wrapper circle");
    await expect(dots.first()).toBeVisible();
    await dots.first().hover({ force: true });
    await expect(
      page.getByText(/🇺🇸 US East|🇪🇺 EU West/).first(),
    ).toBeVisible({ timeout: 5000 });

    // Clicking a dot still pins a result (dot-click resolution works in
    // multi-series mode via the per-region dataKey, not a stale shared index).
    await dots.first().click({ force: true });
    await expect(page.getByTestId("pinned-result-box")).toBeVisible({
      timeout: 5000,
    });
  });

  test("Response Times chart: a down result stays visible via a red gradient stop and an always-on dot above the density threshold", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();
    const downUid = "eu1-down-result";

    // Mock a dense two-region dataset (90 points/region = 180 total, above
    // the 150-point dots-density threshold) with a single down result buried
    // in the middle of the eu-1 series — the exact "outage invisible in a
    // dense Day view" scenario from the spec.
    const pointsPerRegion = 90;
    const intervalMs = 10 * 60_000;
    const downIndex = 45;
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      const points: Record<string, unknown>[] = [];
      for (let i = 0; i < pointsPerRegion; i++) {
        const ts = now - (pointsPerRegion - i) * intervalMs;
        points.push({
          uid: `us1-${i}`,
          durationMs: 55 + (i % 3) * 5,
          status: "up",
          region: "us-1",
          periodStart: new Date(ts).toISOString(),
          periodType: "raw",
        });
        const isDown = i === downIndex;
        points.push({
          uid: isDown ? downUid : `eu1-${i}`,
          durationMs: isDown ? 5000 : 650 + (i % 3) * 10,
          status: isDown ? "down" : "up",
          region: "eu-1",
          periodStart: new Date(ts + 30_000).toISOString(),
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

    await page.route("**/api/v1/orgs/*/regions*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { slug: "us-1", emoji: "🇺🇸", name: "US East" },
            { slug: "eu-1", emoji: "🇪🇺", name: "EU West" },
          ],
          defaultRegions: ["us-1"],
        }),
      })
    );

    await page.route(`**/api/v1/orgs/*/checks/*/results/${downUid}`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid: downUid,
          durationMs: 5000,
          status: "down",
          region: "eu-1",
          periodStart: new Date(now - (pointsPerRegion - downIndex) * intervalMs).toISOString(),
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

    const checkName = `E2E Dense Down Dot ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/dense-down-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".recharts-wrapper")).toBeVisible({ timeout: 15000 });

    // (a) The failing region's own gradient def carries a red (COLOR_DOWN)
    // stop; the healthy region's gradient does not.
    const COLOR_DOWN = "hsl(0, 72%, 51%)";
    const euStopColors = await page
      .locator("#rt-region-eu-1 stop")
      .evaluateAll((els) => els.map((el) => el.getAttribute("stop-color")));
    const usStopColors = await page
      .locator("#rt-region-us-1 stop")
      .evaluateAll((els) => els.map((el) => el.getAttribute("stop-color")));
    expect(euStopColors).toContain(COLOR_DOWN);
    expect(usStopColors).not.toContain(COLOR_DOWN);

    // (b) Despite 180 total points (above the 150-point dots-density
    // threshold), exactly the one down result renders a real per-point dot —
    // every other point renders as an invisible <g/>. Scope to
    // `.recharts-area-dots` (the persistent per-point dot layer) rather than
    // every `circle` in the chart: recharts also renders a transient
    // `.recharts-active-dot` wherever the mouse last happened to hover
    // (including leftover pointer position from prior test steps), which is
    // unrelated to the density-threshold behavior under test.
    const persistentDots = page.locator(".recharts-wrapper .recharts-area-dots circle");
    await expect(persistentDots).toHaveCount(1);
    await expect(persistentDots.first()).toHaveAttribute("fill", COLOR_DOWN);

    // (c) The down dot is clickable and pins its result.
    await persistentDots.first().hover({ force: true });
    await persistentDots.first().click({ force: true });
    await expect(page.getByTestId("pinned-result-box")).toBeVisible({
      timeout: 5000,
    });
  });

  test("Recent Results: region filter narrows the list server-side, cross-filters the chart, and a row badge click selects that region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();
    const requestedRegionParams: (string | null)[] = [];

    // Mock a two-region results response for both the chart window (used to
    // derive the observed-region set) and the Recent Results list itself —
    // the list mock echoes back whatever `region=` it received so the test
    // can assert the param actually reached the API.
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = new URL(route.request().url());
      if (!url.pathname.includes("/results")) return route.continue();
      requestedRegionParams.push(url.searchParams.get("region"));

      const region = url.searchParams.get("region");
      const allPoints = [
        {
          uid: "us1-result",
          durationMs: 55,
          status: "up",
          region: "us-1",
          periodStart: new Date(now - 5 * 60_000).toISOString(),
          periodType: "raw",
        },
        {
          uid: "eu1-result",
          durationMs: 650,
          status: "up",
          region: "eu-1",
          periodStart: new Date(now - 3 * 60_000).toISOString(),
          periodType: "raw",
        },
      ];
      const filtered = region
        ? allPoints.filter((p) => p.region === region)
        : allPoints;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: filtered,
          pagination: { total: filtered.length, size: filtered.length },
        }),
      });
    });

    await page.route("**/api/v1/orgs/*/regions*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { slug: "us-1", emoji: "🇺🇸", name: "US East" },
            { slug: "eu-1", emoji: "🇪🇺", name: "EU West" },
          ],
          defaultRegions: ["us-1"],
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

    const checkName = `E2E Results Filter ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/results-filter-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const resultsFilter = page.getByTestId("results-region-filter");
    await expect(resultsFilter).toBeVisible({ timeout: 15000 });

    // Both rows visible under "All" (no region= param sent).
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(2);

    // The chart observes the same two regions (its own fetch is unfiltered —
    // region isn't part of the chart's query params), so its chip row shows
    // too and starts at 2 points (one per region).
    const chartDataDots = page.locator(".recharts-wrapper circle:has(title)");
    await expect(page.getByTestId("response-time-chart-region-filter")).toBeVisible();
    await expect(chartDataDots).toHaveCount(2);

    // Selecting the US chip narrows the list to one row, cross-filters the
    // chart to that region's single point too, and writes a single
    // ?region=us-1 param (no separate graphRegion/resultsRegion keys).
    await resultsFilter.getByRole("button", { name: /US East/ }).click();
    await page.waitForURL(/[?&]region=us-1(&|$)/);
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(1);
    await expect(chartDataDots).toHaveCount(1);
    expect(page.url()).not.toContain("graphRegion");
    expect(page.url()).not.toContain("resultsRegion");

    // The narrowed request actually carried region=us-1 to the API.
    expect(requestedRegionParams).toContain("us-1");

    // Back to "All" restores both rows (and the chart's two points) and
    // clears the param.
    await resultsFilter.getByRole("button", { name: "All" }).click();
    await page.waitForURL((url) => !url.search.includes("region="));
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(2);
    await expect(chartDataDots).toHaveCount(2);

    // Clicking a row's own region Badge selects that region too.
    const euRow = page.locator('[data-testid^="result-row-"]', {
      hasText: "EU West",
    });
    await euRow.getByTestId(/result-region-badge-/).click();
    await page.waitForURL(/[?&]region=eu-1(&|$)/);
    await expect(page.locator('[data-testid^="result-row-"]')).toHaveCount(1);
    // The badge click must not also trigger the row's own navigate-to-detail
    // onClick (stopPropagation) — still on the check detail page, not a
    // result detail page.
    expect(page.url()).not.toContain("/results/");
  });

  test("Recent Results: duration stats strip shows exact values over raw windows and ~-prefixed values when rollup rows are present", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();

    // Chart-window results (raw only, hour graphPeriod default: "day" tier is
    // raw+hour so use graphPeriod=hour to guarantee an all-raw window) feed
    // both the chart and the shared stats computation.
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = new URL(route.request().url());
      if (!url.pathname.includes("/results")) return route.continue();
      const periodTypeParam = url.searchParams.get("periodType") ?? "";
      const region = url.searchParams.get("region");

      // Raw-only points: durations 40, 60, 80, 100 for us-1 (min 40, max 100,
      // avg 70, p95 = 4th of 4 sorted -> 100 by the same nearest-rank method
      // the aggregator uses: index = floor(4*0.95) = 3 -> last element).
      const rawPoints = [40, 60, 80, 100].map((d, i) => ({
        uid: `us1-raw-${i}`,
        durationMs: d,
        status: "up",
        region: "us-1",
        periodStart: new Date(now - (10 - i) * 60_000).toISOString(),
        periodType: "raw",
      }));

      if (!periodTypeParam.includes("hour") && !periodTypeParam.includes("day")) {
        // Recent Results list call (no periodType filter) — used for the
        // table itself, independent of the stats computation.
        const filtered = region
          ? rawPoints.filter((p) => p.region === region)
          : rawPoints;
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: filtered,
            pagination: { total: filtered.length, size: filtered.length },
          }),
        });
      }

      // Chart-window call (periodType includes hour/day): raw points plus one
      // rollup ("hour") row lacking durationAvgMs entirely, to exercise the
      // durationMs fallback path for the weighted avg.
      const rollupRow = {
        uid: "us1-rollup-1",
        durationMs: 200, // plotted value, used as the avg/p95 fallback
        durationMinMs: 150,
        durationMaxMs: 250,
        totalChecks: 5,
        status: "up",
        region: "us-1",
        periodStart: new Date(now - 3600_000).toISOString(),
        periodType: "hour",
      };
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [...rawPoints, rollupRow],
          pagination: { total: rawPoints.length + 1, size: rawPoints.length + 1 },
        }),
      });
    });

    await page.route("**/api/v1/orgs/*/regions*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [{ slug: "us-1", emoji: "🇺🇸", name: "US East" }],
          defaultRegions: ["us-1"],
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

    const checkName = `E2E Stats Strip ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/stats-strip-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // No region observed yet in the mock's single-region set means the
    // Recent Results header shows no filter row (only >1 region shows it) —
    // this test drives the strip via the URL directly instead, since a
    // single-region "select region" affordance isn't the point under test.
    await page.goto(`${page.url()}?region=us-1&graphPeriod=week`);
    await page.waitForLoadState("networkidle");

    const stats = page.getByTestId("results-duration-stats");
    await expect(stats).toBeVisible({ timeout: 15000 });

    // Rollup row present in the window -> avg and p95 are ~-prefixed
    // estimates; min/max stay exact (min-of-mins/max-of-maxes: raw min 40 vs
    // rollup min 150 -> 40; raw max 100 vs rollup max 250 -> 250).
    await expect(stats).toContainText("40ms");
    await expect(stats).toContainText("250ms");
    await expect(stats.getByText(/~\d/)).toHaveCount(2);
    // Sample count sums raw rows (1 each) + the rollup's totalChecks: 4 + 5 = 9.
    await expect(stats).toContainText("9");
    // Window label reflects the current graphPeriod (week -> "Last week").
    await expect(stats).toContainText(/last week/i);
  });

  test("header shows full labels on desktop and shrinks to icon-only on mobile (never collapses to a menu)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check so we have a detail page to visit. Use a deliberately long
    // name so we exercise title truncation at narrow widths.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Overflow ${Date.now()} ${"Very Long Check Name ".repeat(4)}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/overflow-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // ---- Desktop viewport (>= md): the full inline toolbar is visible and the
    // ⋯ overflow trigger is hidden. ----
    await page.setViewportSize({ width: 1280, height: 800 });

    const moreActions = page.getByRole("button", { name: "More actions" });
    await expect(moreActions).toBeHidden();

    // Each inline action shows its icon + label. The Badges button is an
    // asChild <Link> (anchor) pointing at the badges builder with ?check=<slug>.
    const badgesLink = page.getByLabel("Badges");
    await expect(badgesLink).toBeVisible();
    await expect(badgesLink).toHaveAttribute("href", /\/orgs\/test\/badges\?check=/);
    await expect(badgesLink.getByText("Badges")).toBeVisible();
    await expect(page.getByLabel("Edit").getByText("Edit")).toBeVisible();
    await expect(page.getByLabel("Clone").getByText("Clone")).toBeVisible();
    await expect(page.getByLabel("Refresh").getByText("Refresh")).toBeVisible();
    await expect(page.getByLabel("Delete").getByText("Delete")).toBeVisible();

    // The leading back button stays icon-only (no visible "Back" label).
    const backButton = page.getByRole("button", { name: "Back to checks" });
    await expect(backButton).toBeVisible();

    // ---- Mobile viewport (< lg): the action buttons never collapse into an
    // overflow menu. They stay inline and shrink to icon-only (text labels
    // hide); there is no ⋯ "More actions" trigger. ----
    await page.setViewportSize({ width: 390, height: 812 });

    await expect(backButton).toBeVisible();
    await expect(moreActions).toBeHidden();

    // The inline action buttons remain visible (icon-only); only their text
    // labels are hidden below lg.
    await expect(page.getByLabel("Edit")).toBeVisible();
    await expect(page.getByLabel("Clone")).toBeVisible();
    await expect(page.getByLabel("Refresh")).toBeVisible();
    await expect(page.getByLabel("Delete")).toBeVisible();
    await expect(page.getByLabel("Edit").getByText("Edit")).toBeHidden();
    await expect(page.getByLabel("Delete").getByText("Delete")).toBeHidden();

    // The header must not overflow horizontally even with a long check name.
    const hasOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);

    await page.screenshot({
      path: "test-results/screenshots/check-detail-actions-mobile.png",
      fullPage: true,
    });

    // The inline Delete button opens the confirm dialog directly.
    await page.getByLabel("Delete").click();
    await expect(
      page.getByRole("alertdialog").getByText("Delete Check"),
    ).toBeVisible();
  });

  test("header stacks the action toolbar on its own row below a long title (no horizontal overflow)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a deliberately long-named check — the worst case for header layout.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Stacked ${Date.now()} ${"Very Long Check Name ".repeat(4)}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/stacked-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // ---- Wide desktop: the title block and the action toolbar are on two
    // separate rows, and the page does not overflow horizontally. ----
    await page.setViewportSize({ width: 1280, height: 800 });

    // All six actions plus the back arrow are present with their labels visible.
    const backButton = page.getByRole("button", { name: "Back to checks" });
    await expect(backButton).toBeVisible();
    await expect(page.getByLabel("Edit").getByText("Edit")).toBeVisible();
    await expect(page.getByLabel("Clone").getByText("Clone")).toBeVisible();
    await expect(page.getByLabel("Badges").getByText("Badges")).toBeVisible();
    await expect(page.getByLabel("Refresh").getByText("Refresh")).toBeVisible();
    await expect(page.getByLabel("Delete").getByText("Delete")).toBeVisible();
    // Enable/Disable toggles its label; match either.
    await expect(
      page.getByRole("button", { name: /Disable|Enable/ }),
    ).toBeVisible();

    // The toolbar sits BELOW the title block: a toolbar button's top edge is
    // past the title's bottom edge.
    const titleBox = await page
      .getByRole("heading", { name: checkName })
      .boundingBox();
    const deleteBox = await page.getByLabel("Delete").boundingBox();
    expect(titleBox).not.toBeNull();
    expect(deleteBox).not.toBeNull();
    expect(deleteBox!.y).toBeGreaterThan(titleBox!.y + titleBox!.height);

    // The header itself does not overflow horizontally: the truncating title
    // and the wrapping toolbar fit within the header's own width. Scope the
    // check to the header element — unrelated page content (e.g. a chart that
    // sizes differently in headless CI) must not flake this header assertion.
    const headerOverflow = await page
      .getByTestId("check-detail-header")
      .evaluate((el) => el.scrollWidth > el.clientWidth + 1);
    expect(headerOverflow).toBe(false);
  });

  test("disabling a check turns the header status dot grey and toggling re-colours it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create an enabled HTTP check and land on its detail page.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Detail Dot ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/detail-dot");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    const header = page.getByTestId("check-detail-header");
    const dot = header.getByTestId("check-status-dot");

    // Enabled by default: the dot is not in the disabled state, and there is no
    // "Disabled" badge in the status block.
    await expect(dot).toHaveAttribute("data-disabled", "false");

    // Disable from the header toggle. The dot goes grey (data-disabled="true",
    // overriding the last/live status colour) and the outline "Disabled" badge
    // appears beside the StatusBadge.
    await page.getByRole("button", { name: /Disable|Enable/ }).click();
    await expect(page.getByRole("button", { name: /Enable/ })).toBeVisible();
    await expect(dot).toHaveAttribute("data-disabled", "true");
    await expect(dot).toHaveAttribute("aria-label", "Disabled");
    await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

    await page.screenshot({
      path: "test-results/screenshots/check-detail-disabled-dot.png",
      fullPage: true,
    });

    // Re-enable: the dot flips back out of the disabled state live, no reload.
    await page.getByRole("button", { name: /Enable/ }).click();
    await expect(page.getByRole("button", { name: /Disable/ })).toBeVisible();
    await expect(dot).toHaveAttribute("data-disabled", "false");
  });

  test("direct URL navigation with graphFull=true activates the chart's full-range toggle", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check so we have a detail page to visit.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    const checkName = `E2E Graph Full ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/graph-full-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Regression test for the validateSearch boolean-coercion bug: TanStack
    // Router already parses "?graphFull=true" into a native boolean before
    // validateSearch runs, so a bare `=== "true"` string comparison there
    // always evaluated to false and silently dropped the filter. This only
    // reproduces via a real URL round-trip (in-app clicks build the search
    // object directly), so navigate with page.goto rather than driving the
    // toggle through the UI.
    await page.goto(`${page.url()}?graphFull=true`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByTestId("response-time-chart-full-range-toggle")
    ).toHaveAttribute("data-state", "checked");
  });
});
