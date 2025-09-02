import { test, expect } from "./fixtures";

test.describe("Sidebar User and Org Info", () => {
  test("should display user email in sidebar footer", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    const userMenuButton = page.getByTestId("user-menu-button");
    await expect(userMenuButton).toBeVisible();

    // The test user (test@test.com) has no name set, so email should be the primary display
    await expect(userMenuButton).toContainText("test@test.com");
  });

  test("should display organization name in sidebar header", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();

    // The sidebar header should show the org name "Test Organization"
    await expect(sidebar).toContainText("Test Organization");
  });

  test("should show org switcher with other orgs in user dropdown", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Open user dropdown
    await page.getByTestId("user-menu-button").click();

    // Should show "Switch Organization" label
    await expect(page.getByText("Switch Organization")).toBeVisible();

    // Should show the other two orgs (not the current one)
    await expect(page.getByTestId("switch-org-test2")).toBeVisible();
    await expect(page.getByTestId("switch-org-test3")).toBeVisible();

    // Should show org names
    await expect(page.getByTestId("switch-org-test2")).toContainText(
      "Test Org 2",
    );
    await expect(page.getByTestId("switch-org-test3")).toContainText(
      "Test Org 3",
    );
  });

  test("should switch organization when clicking org in dropdown", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Open user dropdown and click test2
    await page.getByTestId("user-menu-button").click();
    await expect(page.getByTestId("switch-org-test2")).toBeVisible();
    await page.getByTestId("switch-org-test2").click();

    // Wait for navigation to new org
    await page.waitForURL(/\/orgs\/test2/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify sidebar header shows new org name
    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toContainText("Test Org 2");

    // Verify URL contains the new org
    expect(page.url()).toContain("orgs/test2");

    // Wait for any pending dropdown close animations and React reconciliation
    // before reopening the dropdown
    await page.waitForTimeout(500);

    // Open dropdown again -- should now show test and test3, not test2
    const userMenuButton = page.getByTestId("user-menu-button");
    await expect(userMenuButton).toBeVisible();
    await userMenuButton.click();

    // Verify dropdown actually opened by checking for a known item
    await expect(page.getByTestId("logout-button")).toBeVisible({
      timeout: 5000,
    });

    await expect(
      page.getByTestId("switch-org-test3"),
    ).toBeVisible({ timeout: 5000 });
    // Verify current org (test2) is not in the switcher
    await expect(page.getByTestId("switch-org-test2")).not.toBeVisible();
  });

  test("should show fallback icon when user has no avatar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    const userMenuButton = page.getByTestId("user-menu-button");
    await expect(userMenuButton).toBeVisible();

    // Should NOT have an img element (test user has no avatar)
    const avatarImg = userMenuButton.locator("img");
    await expect(avatarImg).not.toBeVisible();

    // Should have the fallback icon container
    const fallbackIcon = userMenuButton.locator(".bg-muted");
    await expect(fallbackIcon).toBeVisible();
  });
});
