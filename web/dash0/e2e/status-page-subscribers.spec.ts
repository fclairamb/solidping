import { test, expect } from "./fixtures";

test.describe("Status page subscribers (admin)", () => {
  test("edit route shows the subscriber list card with a count", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Status Pages" })
      .click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    // Open the edit route for the first status page row.
    const editButton = page
      .locator('[data-testid^="status-page-row-edit-"]')
      .first();
    await expect(editButton).toBeVisible({ timeout: 10000 });
    await editButton.click();
    await page.waitForURL(/\/status-pages\/[^/]+\/edit/);
    await page.waitForLoadState("networkidle");

    // The subscribers card renders with a count badge.
    const card = page.getByTestId("status-page-subscribers");
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("subscriber-count")).toBeVisible();
    await expect(
      card.getByRole("heading", { name: /Subscribers/i }),
    ).toBeVisible();

    await page.screenshot({
      path: "test-results/screenshots/status-page-subscribers.png",
      fullPage: true,
    });
  });
});
