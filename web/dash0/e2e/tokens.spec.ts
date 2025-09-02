import { test, expect } from "./fixtures";

test.describe("Tokens", () => {
  test("should create a token with no expiration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to tokens page
    await page.goto("orgs/test/account/tokens");
    await page.waitForLoadState("networkidle");

    // Click "New Token" button
    await page.getByTestId("new-token-button").click();

    // Fill in token name
    const tokenName = `no-expiry-${Date.now()}`;
    await page.getByTestId("token-name-input").fill(tokenName);

    // Select "No expiration"
    await page.getByTestId("token-expiry-select").click();
    await page.getByRole("option", { name: "No expiration" }).click();

    // Click Create
    await page.getByTestId("token-create-button").click();

    // Wait for the created token value to appear
    await expect(page.getByTestId("token-created-value")).toBeVisible();

    // Close the dialog
    await page.getByTestId("token-created-done").click();
    await page.waitForLoadState("networkidle");

    // Verify the token appears in the table with "Never" expiry
    const tokenRow = page.locator("tr", { hasText: tokenName });
    await expect(tokenRow).toBeVisible();
    await expect(tokenRow.getByTestId("token-expiry")).toHaveText("Never");
  });

  test("should create a token with expiration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to tokens page
    await page.goto("orgs/test/account/tokens");
    await page.waitForLoadState("networkidle");

    // Click "New Token" button
    await page.getByTestId("new-token-button").click();

    // Fill in token name
    const tokenName = `with-expiry-${Date.now()}`;
    await page.getByTestId("token-name-input").fill(tokenName);

    // Keep default expiry (90 days) or select explicitly
    await page.getByTestId("token-expiry-select").click();
    await page.getByRole("option", { name: "90 days" }).click();

    // Click Create
    await page.getByTestId("token-create-button").click();

    // Wait for the created token value to appear
    await expect(page.getByTestId("token-created-value")).toBeVisible();

    // Close the dialog
    await page.getByTestId("token-created-done").click();
    await page.waitForLoadState("networkidle");

    // Verify the token appears in the table with a date (not "Never")
    const tokenRow = page.locator("tr", { hasText: tokenName });
    await expect(tokenRow).toBeVisible();
    const expiryCell = tokenRow.getByTestId("token-expiry");
    await expect(expiryCell).not.toHaveText("Never");
    await expect(expiryCell).not.toHaveText("Expired");
  });

  test("should revoke a token", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to tokens page
    await page.goto("orgs/test/account/tokens");
    await page.waitForLoadState("networkidle");

    // Create a token to revoke
    await page.getByTestId("new-token-button").click();
    const tokenName = `revoke-${Date.now()}`;
    await page.getByTestId("token-name-input").fill(tokenName);
    await page.getByTestId("token-create-button").click();
    await expect(page.getByTestId("token-created-value")).toBeVisible();
    await page.getByTestId("token-created-done").click();
    await page.waitForLoadState("networkidle");

    // Find the token row and click its revoke button
    const tokenRow = page.locator("tr", { hasText: tokenName });
    await expect(tokenRow).toBeVisible();
    await tokenRow.getByTestId("token-revoke-button").click();

    // Confirm revocation
    await page.getByTestId("token-revoke-confirm").click();
    await page.waitForLoadState("networkidle");

    // Verify the token is gone
    await expect(page.locator("tr", { hasText: tokenName })).not.toBeVisible();
  });
});
