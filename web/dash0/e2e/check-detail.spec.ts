import { test, expect } from "./fixtures";

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

    // The check page polls every 1.5s for its first result (fast-poll window,
    // up to 30s) — wait for the first Recent Results row to appear.
    const firstRow = page.locator('[data-testid^="result-row-"]').first();
    await expect(firstRow).toBeVisible({ timeout: 30000 });

    // The Region cell must render the actual region the result ran from, not
    // the "-" placeholder used only for legacy rows with no stored region.
    // Single-worker local/e2e setups register under region "default", which
    // has no configured region-definition system parameter, so it renders
    // via the built-in fallback definition (📍 Default) as an outline Badge.
    const regionCell = firstRow.getByTestId("result-region-cell");
    await expect(regionCell).toBeVisible();
    await expect(regionCell).not.toHaveText("-");
    await expect(regionCell.getByText(/default/i)).toBeVisible();
  });

  test("Response Times chart: region chips filter to a single region's points", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = Date.now();

    // Mock a multi-region results response: a low-latency "us-1" cluster and
    // a high-latency "eu-1" cluster, interleaved in time — the exact shape
    // that renders as a cross-region sawtooth without the filter.
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

    await expect(page.locator(".recharts-wrapper")).toBeVisible();
    const allDotCount = await page.locator(".recharts-wrapper circle").count();

    // Selecting the US chip narrows the chart to fewer points (only the
    // us-1 cluster) and writes ?graphRegion=us-1 into the URL.
    await usChip.click();
    await page.waitForURL(/graphRegion=us-1/);
    await expect(page.locator(".recharts-wrapper circle")).toHaveCount(
      allDotCount / 2,
    );

    // Switching to the EU chip updates the URL and keeps a single-region
    // point count (same size class as the US selection, different region).
    await euChip.click();
    await page.waitForURL(/graphRegion=eu-1/);
    await expect(page.locator(".recharts-wrapper circle")).toHaveCount(
      allDotCount / 2,
    );

    // Back to "All regions" restores every point and clears the param.
    await allChip.click();
    await expect(page.locator(".recharts-wrapper circle")).toHaveCount(
      allDotCount,
    );
    expect(page.url()).not.toContain("graphRegion");
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
});
