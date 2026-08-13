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

    // Verify Account group. The group heading itself reads "Account", same as
    // the new section-landing entry's title, so scope the heading assertion
    // to the [cmdk-group-heading] element to avoid a strict-mode ambiguity.
    await expect(page.locator("[cmdk-group-heading]", { hasText: "Account" })).toBeVisible();
    await expect(page.getByTestId("command-menu-account")).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Profile" })).toBeVisible();
    await expect(page.locator('[cmdk-item]').filter({ hasText: "Tokens" })).toBeVisible();
    await expect(page.getByTestId("command-menu-ai")).toBeVisible();
    await expect(page.getByTestId("command-menu-ai")).toContainText("AI assistants");

    // Verify Organization group (same heading/entry-title collision as above).
    await expect(page.locator("[cmdk-group-heading]", { hasText: "Organization" })).toBeVisible();
    await expect(page.getByTestId("command-menu-organization")).toBeVisible();
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

  test("should expose a New Check action and navigate to /checks/new", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Actions group should be shown with the New Check entry
    await expect(page.getByText("Actions", { exact: true })).toBeVisible();
    const newCheckItem = page.getByTestId("command-menu-new-check");
    await expect(newCheckItem).toBeVisible();
    await expect(newCheckItem).toContainText("New Check");
    await expect(newCheckItem).toContainText("Create a new monitoring check");

    // Filter by typing — entry should still be reachable
    await input.fill("new check");
    await expect(newCheckItem).toBeVisible();

    // Selecting it should navigate to /checks/new
    await newCheckItem.click();
    await page.waitForURL(/\/checks\/new/, { timeout: 5000 });
    expect(page.url()).toContain("/checks/new");
    await expect(input).not.toBeVisible();
  });

  test("exposes the AI assistants (MCP) entry, findable by MCP, → /account/mcp", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // The entry lives in the Account group and matches on "MCP" even though
    // its title reads "AI assistants" — the description carries the MCP term.
    const aiItem = page.getByTestId("command-menu-ai");
    await input.fill("MCP");
    await expect(aiItem).toBeVisible();

    // Selecting it navigates to the Account MCP page.
    await aiItem.click();
    await page.waitForURL(/\/account\/mcp/, { timeout: 5000 });
    expect(page.url()).toContain("/account/mcp");
    await expect(input).not.toBeVisible();
    await expect(
      page.getByRole("heading", { name: /ai assistants/i }),
    ).toBeVisible();
  });

  test("exposes the Organization entry, findable by 'organization', → parent redirects to invitations", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Searching "organization" surfaces the section landing entry (not just
    // its sub-pages, whose titles don't contain the word).
    const orgItem = page.getByTestId("command-menu-organization");
    await input.fill("organization");
    await expect(orgItem).toBeVisible();
    await expect(orgItem).toContainText("Organization");

    // Selecting it navigates to the parent route, whose index redirects to
    // the current default child (invitations).
    await orgItem.click();
    await page.waitForURL(/\/organization\/invitations/, { timeout: 5000 });
    expect(page.url()).toContain("/organization/invitations");
    await expect(input).not.toBeVisible();
  });

  test("exposes the Account entry, findable by 'account', → parent redirects to profile", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await page.keyboard.press("Meta+k");
    const input = page.locator('[cmdk-input]');
    await expect(input).toBeVisible({ timeout: 3000 });

    // Searching "account" surfaces the section landing entry (not just its
    // sub-pages, whose titles don't contain the word).
    const accountItem = page.getByTestId("command-menu-account");
    await input.fill("account");
    await expect(accountItem).toBeVisible();
    await expect(accountItem).toContainText("Account");

    // Selecting it navigates to the parent route, whose index redirects to
    // the current default child (profile).
    await accountItem.click();
    await page.waitForURL(/\/account\/profile/, { timeout: 5000 });
    expect(page.url()).toContain("/account/profile");
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
