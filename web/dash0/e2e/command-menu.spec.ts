import { test, expect } from "./fixtures";

test.describe("Command Menu (Cmd+K)", () => {
  test("should open command menu with Cmd+K and show pages", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Open command menu with Cmd+K
    await page.keyboard.press("Meta+k");

    // Wait for the command menu dialog to appear
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Verify pages group is shown
    await expect(page.getByText("Pages", { exact: true })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Dashboard" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Checks" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Incidents" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Events" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Status Pages" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Badges" })).toBeVisible();

    // Verify Account group
    await expect(page.getByText("Account", { exact: true })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Profile" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Tokens" })).toBeVisible();

    // Verify Organization group
    await expect(page.getByText("Organization", { exact: true })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Members" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Invitations" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Settings" })).toBeVisible();

    // Take screenshot of command menu open with pages
    await page.screenshot({
      path: "test-results/screenshots/command-menu-open.png",
      fullPage: true,
    });
  });

  test("should filter results when typing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Open command menu
    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Type to filter
    await input.fill("inci");

    // Should show Incidents, hide others
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Incidents" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Dashboard" })).not.toBeVisible();

    // Take screenshot of filtered results
    await page.screenshot({
      path: "test-results/screenshots/command-menu-filtered.png",
      fullPage: true,
    });
  });

  test("should navigate to a page when selected", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Open command menu and select Incidents
    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    await input.fill("Incidents");
    await page.locator('[cmdk-item]').filter({ hasText: "Incidents" }).click();

    // Should navigate to incidents page
    await page.waitForURL(/\/incidents/, { timeout: 5000 });
    expect(page.url()).toContain("/incidents");
  });

  test("should navigate to members via keyboard (type + Enter)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    await input.pressSequentially("members", { delay: 50 });
    await page.keyboard.press("Enter");

    await page.waitForURL(/\/organization\/members/, { timeout: 5000 });
    expect(page.url()).toContain("/organization/members");
    await expect(input).not.toBeVisible();
  });

  test("should close on Enter when no items match", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    await input.pressSequentially("zzznomatchzz", { delay: 30 });
    await page.keyboard.press("Enter");

    await expect(input).not.toBeVisible();
  });

  test("should close on Escape", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Open command menu
    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Press Escape
    await page.keyboard.press("Escape");

    // Dialog should be gone
    await expect(input).not.toBeVisible();
  });

  test("should show checks and navigate to a check", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // First create a check so we have one to search for
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `CmdK Test ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/cmdk-test");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Now go back to dashboard and open command menu
    await page.getByRole("link", { name: "Dashboard" }).click();
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Type the check name to filter
    await input.fill("CmdK");

    // Should show the check in results
    const checkItem = page.locator('[cmdk-item]').filter({ hasText: checkName });
    await expect(checkItem).toBeVisible({ timeout: 5000 });

    // Take screenshot showing check in command menu
    await page.screenshot({
      path: "test-results/screenshots/command-menu-check.png",
      fullPage: true,
    });

    // Click the check to navigate
    await checkItem.click();

    // Should navigate to the check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 5000 });
    expect(page.url()).toContain("/checks/");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();
  });
});
