import { test, expect } from "./fixtures";

test.describe("Status page custom domain", () => {
  test("set a domain shows the DNS records and an unverified chip", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page.getByRole("link", { name: "Status Pages" }).click();
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

    // The custom-domain card renders.
    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    // Enter a domain that does not shadow any of the installation's own hosts
    // and save it.
    const domain = `status-e2e-${Date.now()}.example.com`;
    await card.getByTestId("custom-domain-input").fill(domain);
    await card.getByTestId("custom-domain-save").click();

    // After saving, the two DNS records (CNAME + TXT) and an Unverified chip
    // appear, and a Verify button is available.
    const records = card.getByTestId("custom-domain-records");
    await expect(records).toBeVisible({ timeout: 10000 });
    await expect(records).toContainText("CNAME");
    await expect(records).toContainText("_solidping-challenge");
    await expect(card.getByTestId("custom-domain-verify")).toBeVisible();
    await expect(card).toContainText(/Unverified/i);

    await page.screenshot({
      path: "test-results/screenshots/status-page-custom-domain.png",
      fullPage: true,
    });
  });
});
