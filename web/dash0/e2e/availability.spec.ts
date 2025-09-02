import { test, expect } from "./fixtures";

test.describe("Availability Table", () => {
  test("should show correct percentage from aggregated data", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Intercept results API - useAllResults fetches all period types in one call
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      // Don't intercept non-results API calls
      if (!url.includes("/results")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { uid: "r1", availabilityPct: 99.97, periodStart: new Date().toISOString(), periodType: "hour", status: "up" },
            { uid: "r2", availabilityPct: 99.97, periodStart: new Date().toISOString(), periodType: "day", status: "up" },
            { uid: "r3", availabilityPct: 99.97, periodStart: new Date().toISOString(), periodType: "month", status: "up" },
          ],
          pagination: { total: 3, size: 3 },
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

    // Create a check and navigate to its detail page
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    const checkName = `E2E Avail ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/avail-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify availability table shows the mocked percentage
    await expect(page.getByText("99.9700%").first()).toBeVisible({ timeout: 10000 });
  });

  test("should show 0s for downtime when availability is 100%", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Intercept results API - single call returns all period types
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { uid: "r1", availabilityPct: 100, periodStart: new Date().toISOString(), periodType: "hour", status: "up" },
            { uid: "r2", availabilityPct: 100, periodStart: new Date().toISOString(), periodType: "day", status: "up" },
            { uid: "r3", availabilityPct: 100, periodStart: new Date().toISOString(), periodType: "month", status: "up" },
          ],
          pagination: { total: 3, size: 3 },
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

    const checkName = `E2E Downtime ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/downtime-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify downtime shows "0s" not "none"
    const downtimeCells = page.locator("td").filter({ hasText: "0s" });
    await expect(downtimeCells.first()).toBeVisible({ timeout: 10000 });
  });

  test("should compute availability from raw results when no aggregated data", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Return only raw results (no aggregated) - 5 up, 5 down = 50%
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { uid: "raw1", status: "up", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw2", status: "up", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw3", status: "up", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw4", status: "up", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw5", status: "up", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw6", status: "down", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw7", status: "down", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw8", status: "down", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw9", status: "down", periodStart: new Date().toISOString(), periodType: "raw" },
            { uid: "raw10", status: "down", periodStart: new Date().toISOString(), periodType: "raw" },
          ],
          pagination: { total: 10, size: 10 },
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

    const checkName = `E2E Raw Fallback ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/raw-fallback");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Should show 50% computed from raw results
    await expect(page.getByText("50.0000%").first()).toBeVisible({ timeout: 10000 });
  });

  test("should show dash when no data at all", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // All empty
    await page.route("**/api/v1/orgs/*/results*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
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

    const checkName = `E2E No Data ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/no-data");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Availability table should show "-" for availability (not 100%)
    const availabilityTable = page.locator("table").filter({ hasText: "Time period" });
    await expect(availabilityTable).toBeVisible({ timeout: 10000 });

    // The "Today" row should show "-" for availability
    const todayRow = availabilityTable.locator("tr").filter({ hasText: "Today" });
    await expect(todayRow.locator("td").nth(1)).toHaveText("-");
  });
});
