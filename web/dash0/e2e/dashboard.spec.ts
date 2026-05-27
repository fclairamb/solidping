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

  test("Recent activity footer shows a single arrow", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    const footer = page.getByTestId("recent-activity-footer");
    // The recent-activity card only renders for a non-empty org. When the
    // seeded test org has checks this is visible; otherwise skip the assertion.
    if (await footer.isVisible().catch(() => false)) {
      const text = (await footer.innerText()).trim();
      // Exactly one right-arrow — the duplicate "→ →" bug is gone.
      const arrowCount = (text.match(/→/g) || []).length;
      expect(arrowCount).toBe(1);
      expect(text).toMatch(/→$/);
    }
  });

  test("recent activity renders resolved labels and activation descriptions", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const API_BASE = "http://localhost:4000";

    // Authenticate against the API to emit the activation milestone directly.
    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const token = (await loginResp.json()).accessToken as string;

    // Creating the first check emits org.activation.first_check_created, which
    // surfaces in the dashboard's recent-activity feed with its description.
    await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: "Activation feed probe",
        type: "http",
        config: { url: "https://example.com" },
      },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const feed = page.getByTestId("recent-activity-footer");
    // Recent-activity card renders for a non-empty org (we just created a check).
    await expect(feed).toBeVisible({ timeout: 10000 });

    // Labels must be resolved — no raw event keys leak into the feed.
    await expect(page.getByText("org.activation.first_check_created")).toHaveCount(0);

    // When the activation milestone is in the feed, its description renders.
    const desc = page.getByText("Your first uptime check is live and monitoring.");
    const label = page.getByText("First Check Created", { exact: true });
    if (await label.first().isVisible().catch(() => false)) {
      await expect(desc.first()).toBeVisible();
    }
  });

  test("KPI tiles navigate to the right list pages", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Monitored tile → checks list
    await page.getByTestId("kpi-tile-monitored").click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/checks\/?$/);

    await page.goBack();
    await page.waitForLoadState("networkidle");

    // Down tile → checks list filtered to failing statuses
    await page.getByTestId("kpi-tile-down").click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toContain("/orgs/test/checks");
    expect(page.url()).toContain("status=down");

    await page.goBack();
    await page.waitForLoadState("networkidle");

    // Active incidents tile → incidents list with state=active
    await page.getByTestId("kpi-tile-incidents").click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toContain("/orgs/test/incidents");
    expect(page.url()).toContain("state=active");

    await page.goBack();
    await page.waitForLoadState("networkidle");

    // Availability tile — must NOT navigate
    const availabilityTile = page.getByTestId("kpi-tile-availability");
    await expect(availabilityTile).toBeVisible();
    const tag = await availabilityTile.evaluate((el) => el.tagName.toLowerCase());
    expect(tag).not.toBe("a");
  });
});
