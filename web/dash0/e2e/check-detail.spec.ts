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
});
