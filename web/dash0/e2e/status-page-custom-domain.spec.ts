import { test, expect } from "./fixtures";

test.describe("Status page custom domain", () => {
  test("set a domain shows a single CNAME record and an unverified chip", async ({
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

    // v0.8.0 contract: exactly ONE DNS record, a CNAME. The TXT ownership
    // challenge is gone, so none of its markers may appear on the card.
    const records = card.getByTestId("custom-domain-records");
    await expect(records).toBeVisible({ timeout: 10000 });
    await expect(records).toContainText("CNAME");
    await expect(records).not.toContainText("_solidping-challenge");
    await expect(records).not.toContainText("sp-domain-verify");
    await expect(records).not.toContainText("TXT");

    // One record row => the "Type" label appears exactly once.
    await expect(records.getByText("Type", { exact: true })).toHaveCount(1);

    // The record is named after the customer's domain.
    await expect(records).toContainText(domain);

    // The value carries a copy-to-clipboard button.
    await expect(
      records.getByRole("button", { name: /copy record value/i })
    ).toBeVisible();

    // Verify button and the unverified chip.
    await expect(card.getByTestId("custom-domain-verify")).toBeVisible();
    await expect(card).toContainText(/Unverified/i);

    // The certificate chip only appears once the domain is verified AND the
    // server terminates TLS itself, so it must be absent here.
    await expect(card.getByTestId("custom-domain-cert-status")).toHaveCount(0);

    await page.screenshot({
      path: "test-results/screenshots/status-page-custom-domain.png",
      fullPage: true,
    });
  });

  test("verifying a domain with no DNS keeps it unverified and certless", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    const editButton = page
      .locator('[data-testid^="status-page-row-edit-"]')
      .first();
    await expect(editButton).toBeVisible({ timeout: 10000 });
    await editButton.click();
    await page.waitForURL(/\/status-pages\/[^/]+\/edit/);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    const domain = `status-verify-${Date.now()}.example.com`;
    await card.getByTestId("custom-domain-input").fill(domain);
    await card.getByTestId("custom-domain-save").click();
    await expect(card.getByTestId("custom-domain-records")).toBeVisible({
      timeout: 10000,
    });

    // The domain has no CNAME in real DNS, so verification must fail: the chip
    // stays Unverified and no certificate chip appears (nothing was issued).
    await card.getByTestId("custom-domain-verify").click();
    await expect(card).toContainText(/Unverified/i, { timeout: 20000 });
    await expect(card.getByTestId("custom-domain-cert-status")).toHaveCount(0);
  });
});
