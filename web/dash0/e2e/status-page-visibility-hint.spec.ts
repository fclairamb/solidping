import { test, expect } from "./fixtures";

// Spec 2026-10-28-08: the visibility hint is described-by the select, and with
// "password" selected both the hint and the password field are visible.
test.describe("Status page visibility hint", () => {
  test("password visibility shows the hint and the password field", async ({
    authenticatedPage: page,
  }) => {
    await page.goto(`orgs/test/status-pages/new`);
    const trigger = page.locator("#visibility");
    await expect(trigger).toBeVisible({ timeout: 30000 });
    await expect(trigger).toHaveAttribute(
      "aria-describedby",
      "status-page-visibility-hint",
    );

    await trigger.click();
    await page.getByRole("option", { name: /password/i }).click();

    const hint = page.getByTestId("status-page-visibility-hint");
    await expect(hint).toBeVisible();
    await expect(hint).toContainText("Set it below.");
    await expect(page.locator("#status-page-password")).toBeVisible();
  });
});
