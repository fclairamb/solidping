import { test, expect } from "@playwright/test";

const BASE = "http://localhost:4000";

test.describe("Public status page", () => {
  test("default org URL renders status page (not blank)", async ({ page }) => {
    await page.goto(`${BASE}/status0/default`);
    await page.waitForLoadState("networkidle");

    // React app must have mounted — root must not be empty
    const rootKids = await page.evaluate(
      () => document.getElementById("root")?.children?.length ?? 0,
    );
    expect(rootKids).toBeGreaterThan(0);

    // Should not show the 404 / not-found message
    await expect(page.getByText("Status Page Not Found")).not.toBeVisible();

    // Should show the status page name for the default page
    await expect(page.getByText("Status 0")).toBeVisible({ timeout: 10000 });

    await page.screenshot({
      path: "test-results/screenshots/status-page-default-org.png",
      fullPage: true,
    });
  });

  test("slug URL renders the named status page (not blank)", async ({
    page,
  }) => {
    await page.goto(`${BASE}/status0/default/status-0`);
    await page.waitForLoadState("networkidle");

    // React app must have mounted — root must not be empty
    const rootKids = await page.evaluate(
      () => document.getElementById("root")?.children?.length ?? 0,
    );
    expect(rootKids).toBeGreaterThan(0);

    // Should not show the 404 / not-found message
    await expect(page.getByText("Status Page Not Found")).not.toBeVisible();

    // Should show the correct page name
    await expect(page.getByText("Status 0")).toBeVisible({ timeout: 10000 });

    await page.screenshot({
      path: "test-results/screenshots/status-page-slug.png",
      fullPage: true,
    });
  });

  test("root URL renders the index page", async ({ page }) => {
    await page.goto(`${BASE}/status0/`);
    await page.waitForLoadState("networkidle");

    const rootKids = await page.evaluate(
      () => document.getElementById("root")?.children?.length ?? 0,
    );
    expect(rootKids).toBeGreaterThan(0);

    await page.screenshot({
      path: "test-results/screenshots/status-page-index.png",
      fullPage: true,
    });
  });
});
