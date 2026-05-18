import { test, expect } from "@playwright/test";

test.describe("Network Discovery", () => {
  test.beforeEach(async ({ page }) => {
    // Log in with test credentials.
    await page.goto("/dash0/orgs/test/login");
    await page.fill('input[name="email"]', "test@test.com");
    await page.fill('input[name="password"]', "test");
    await page.click('button[type="submit"]');
    await page.waitForURL(/\/orgs\/test\//);
  });

  test("discovery sidebar link is visible for admin", async ({ page }) => {
    await page.goto("/dash0/orgs/test");
    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();
    // Discovery link should appear in the sidebar.
    const discoveryLink = page.getByRole("link", { name: /discovery/i });
    await expect(discoveryLink).toBeVisible();
  });

  test("can navigate to discovery index", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery");
    await expect(page.getByRole("heading", { name: /network discovery/i })).toBeVisible();
    await expect(page.getByRole("link", { name: /new scan|start new scan/i })).toBeVisible();
  });

  test("can navigate to new scan form", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await expect(page.getByLabel(/cidr/i)).toBeVisible();
    await expect(page.getByRole("checkbox")).toBeVisible();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
  });

  test("start scan button is disabled without confirmation", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    // Confirmation not checked — submit should be disabled.
    await expect(page.getByRole("button", { name: /start scan/i })).toBeDisabled();
  });

  test("start scan button enables after confirmation", async ({ page }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    await page.fill("textarea", "127.0.0.1/32");
    await page.getByRole("checkbox").check();
    await expect(page.getByRole("button", { name: /start scan/i })).toBeEnabled();
  });
});
