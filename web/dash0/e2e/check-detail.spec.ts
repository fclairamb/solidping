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
});
