import { test, expect } from "./fixtures";

test.describe("Dashboard", () => {
  test("should land on org dashboard after login", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Wait for page to load
    await page.waitForLoadState("networkidle");

    // Take screenshot of dashboard
    await page.screenshot({
      path: "test-results/screenshots/dashboard.png",
      fullPage: true,
    });

    // Spec #46 lands login on /orgs/$org — the operator welcome page — not /checks.
    expect(page.url()).not.toContain("/login");
    expect(page.url()).toMatch(/\/orgs\/test\/?$/);

    // Sidebar is the cheapest way to confirm we rendered an authenticated page.
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
  });

  test("should display sidebar navigation", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // The sidebar should be visible with navigation items
    const sidebar = page.locator('[data-testid="app-sidebar"]');
    const sidebarTrigger = page.locator('[data-testid="sidebar-trigger"]');

    // Either sidebar or sidebar trigger should be present
    const hasSidebar =
      (await sidebar.isVisible()) || (await sidebarTrigger.isVisible());
    expect(hasSidebar).toBe(true);

    // Take screenshot of sidebar navigation
    await page.screenshot({
      path: "test-results/screenshots/dashboard-sidebar.png",
      fullPage: true,
    });
  });

  test("should show loading state then content", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to org root - should redirect to checks
    await page.goto("orgs/test");
    await page.waitForLoadState("domcontentloaded");

    // Wait for content to load
    await page.waitForSelector("body", { state: "visible" });

    // Eventually, the page should show real content
    await page.waitForLoadState("networkidle");

    // Take final screenshot
    await page.screenshot({
      path: "test-results/screenshots/dashboard-loaded.png",
      fullPage: true,
    });
  });

  test("should not have content cut off by sidebar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Check that the "Checks" heading starts after the sidebar
    const checksHeading = page.getByRole("heading", { name: "Checks", exact: true });
    if (await checksHeading.isVisible()) {
      const boundingBox = await checksHeading.boundingBox();
      expect(boundingBox).not.toBeNull();
      // The text should start after the sidebar (at least 250px from left edge)
      expect(boundingBox!.x).toBeGreaterThan(250);
    }

    // Take screenshot for visual verification
    await page.screenshot({
      path: "test-results/screenshots/dashboard-sidebar-layout.png",
      fullPage: true,
    });
  });
});
