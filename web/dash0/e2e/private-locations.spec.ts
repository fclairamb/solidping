import { test, expect, API_BASE, type Page } from "./fixtures";

// E2E for the Private locations page (spec 2026-07-16-02): create a private
// region, mint an enrollment token (shown once), see it in the check-form
// region picker, and clean up.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

// Remove any leftover region from a previous run so the suite is idempotent.
async function deleteRegionIfExists(page: Page, token: string, slug: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Private locations", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteRegionIfExists(authenticatedPage, token, "e2e-dc");
  });

  test.afterEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteRegionIfExists(authenticatedPage, token, "e2e-dc");
  });

  test("create region, mint one-shot token, delete region", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");

    // Empty state (or at least the create form) renders.
    await expect(page.getByTestId("private-regions-card")).toBeVisible();

    // Create a private region.
    await page.getByTestId("new-region-slug").fill("e2e-dc");
    await page.getByTestId("new-region-name").fill("E2E Datacenter");
    await page.getByTestId("create-region").click();

    const row = page.getByTestId("private-region-e2e-dc");
    await expect(row).toBeVisible();
    // The fully-qualified region string is shown (the reserved @-namespace).
    await expect(row).toContainText("@test/e2e-dc");

    // Mint an enrollment token: the secret is revealed exactly once.
    await page.getByTestId("mint-token-e2e-dc").click();
    const minted = page.getByTestId("minted-token");
    await expect(minted).toBeVisible();
    await expect(minted).toContainText("spe_");

    // Close the reveal dialog; the pending-token list shows the token row
    // WITHOUT the secret.
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("pending-tokens-card")).toBeVisible();
    await expect(page.getByTestId("pending-tokens-card")).toContainText("@test/e2e-dc");
    await expect(page.getByTestId("pending-tokens-card")).not.toContainText("spe_");

    // Cancel the pending token (destructive icon), then delete the region.
    await page
      .getByTestId("pending-tokens-card")
      .locator("[data-testid^='delete-token-']")
      .first()
      .click();

    await page.getByTestId("delete-region-e2e-dc").click();
    await page.getByTestId("confirm-delete-region").click();
    await expect(page.getByTestId("private-region-e2e-dc")).not.toBeVisible();
  });

  test("private region appears in the check-form region picker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create the region via API for speed.
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug: "e2e-dc", name: "E2E Datacenter" },
    });
    expect(resp.ok()).toBeTruthy();

    await page.goto("orgs/test/checks/new");
    await page.waitForLoadState("networkidle");

    // Pick a check type so the form (and its region picker) renders.
    await page.getByText("HTTP", { exact: false }).first().click();

    // The private region is offered under its fully-qualified slug with the
    // Private badge.
    const option = page.getByTestId("region-option-@test/e2e-dc");
    await expect(option).toBeVisible();
    await expect(option).toContainText("E2E Datacenter");
    await expect(option).toContainText("Private");
  });
});
