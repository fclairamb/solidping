import { test, expect } from "./fixtures";

test.describe("Checks", () => {
  test("should display the checks list page", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Click on Checks in the sidebar to navigate
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();

    // Wait for navigation to complete
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");

    // Verify we're on the checks page
    expect(page.url()).toContain("/checks");

    // Check for page title
    await expect(page.getByRole("heading", { name: "Checks", exact: true })).toBeVisible();

    // Check for "New Check" button
    await expect(page.getByTestId("new-check-button")).toBeVisible();

    // Wait for checks data to finish loading (either check items or empty state)
    await Promise.race([
      page.getByText("No checks configured yet").waitFor({ state: "visible", timeout: 5000 }).catch(() => {}),
      page.locator("table tbody tr, [data-testid='check-card']").first().waitFor({ state: "visible", timeout: 5000 }).catch(() => {}),
    ]);
    await page.waitForLoadState("networkidle");

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/checks-list.png",
      fullPage: true,
    });
  });

  test("should navigate to new check form", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to checks page via sidebar
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");

    // Wait for the "New Check" button to be visible
    await expect(page.getByTestId("new-check-button")).toBeVisible();

    // Click "New Check" button
    await page.getByTestId("new-check-button").click();

    // Wait for navigation
    await page.waitForURL(/\/checks\/new/);

    // Verify we're on the new check page
    expect(page.url()).toContain("/checks/new");

    // Check for form elements
    await expect(
      page.getByRole("heading", { name: "New Check" })
    ).toBeVisible();
    await expect(page.getByTestId("check-type-select")).toBeVisible();
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await expect(page.getByTestId("check-submit-button")).toBeVisible();

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/checks-new-form.png",
      fullPage: true,
    });
  });

  test("should load check detail page on direct URL access", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check so we have a known detail page
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Direct ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/direct-test");
    await page.getByTestId("check-submit-button").click();

    // Wait for check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    const checkDetailUrl = page.url();

    // Direct navigation (simulates page refresh / bookmark / direct access)
    await page.goto(checkDetailUrl);
    await page.waitForLoadState("networkidle");

    // Should still be on the check detail page, not redirected
    expect(page.url()).toBe(checkDetailUrl);
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Take screenshot of check detail via direct URL
    await page.screenshot({
      path: "test-results/screenshots/checks-detail-direct.png",
      fullPage: true,
    });
  });

  test("should create a new HTTP check", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to checks page, then new check form
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Generate a unique check name and URL to avoid slug conflicts
    const timestamp = Date.now();
    const randomSuffix = Math.random().toString(36).substring(7);
    const checkName = `E2E Create ${timestamp}`;
    const checkUrl = `https://httpbin.org/anything/${timestamp}-${randomSuffix}`;

    // Fill in the form (HTTP is default type)
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill(checkUrl);

    // Take screenshot before submit
    await page.screenshot({
      path: "test-results/screenshots/checks-new-filled.png",
      fullPage: true,
    });

    // Submit the form
    await page.getByTestId("check-submit-button").click();

    // Wait for navigation to check detail page (UUID format, not /new)
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });

    // Verify we're on the check detail page
    expect(page.url()).toMatch(/\/checks\/[0-9a-f-]+$/);

    // Verify the check name is displayed
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Take screenshot of created check
    await page.screenshot({
      path: "test-results/screenshots/checks-created.png",
      fullPage: true,
    });
  });

  test("should create a heartbeat check with 7 days interval", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to new check form
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Select Heartbeat type
    await page.getByTestId("check-type-select").click();
    await page.getByRole("option", { name: /Heartbeat/i }).click();

    // Fill in the name
    const checkName = `E2E Heartbeat ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);

    // Set period to 7 days
    await page.getByTestId("check-period-input").fill("7");
    await page.getByTestId("check-period-unit-select").click();
    await page.getByRole("option", { name: "Days" }).click();

    // Take screenshot before submit
    await page.screenshot({
      path: "test-results/screenshots/checks-heartbeat-form.png",
      fullPage: true,
    });

    // Submit the form
    await page.getByTestId("check-submit-button").click();

    // Wait for navigation to check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify check name is displayed
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Verify the heartbeat endpoint section is visible
    await expect(page.getByText("Heartbeat Endpoint")).toBeVisible();

    // Verify curl command label is shown
    await expect(page.getByText("Sample curl command:")).toBeVisible();

    // Take screenshot of heartbeat detail page
    await page.screenshot({
      path: "test-results/screenshots/checks-heartbeat-created.png",
      fullPage: true,
    });
  });

  test("should preserve heartbeat token after editing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a heartbeat check
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("check-type-select").click();
    await page.getByRole("option", { name: /Heartbeat/i }).click();

    const checkName = `E2E Token Test ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);

    await page.getByTestId("check-submit-button").click();

    // Wait for check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();
    await expect(page.getByText("Heartbeat Endpoint")).toBeVisible();

    // Capture the heartbeat URL containing the token
    const heartbeatUrlElement = page.locator(".font-mono.break-all span").first();
    const heartbeatUrlBefore = await heartbeatUrlElement.textContent();
    expect(heartbeatUrlBefore).toContain("token=");

    // Navigate to edit page
    await page.locator('a[href*="/edit"]').click();
    await page.waitForURL(/\/edit$/);
    await page.waitForLoadState("networkidle");

    // Change the name
    const updatedName = `${checkName} Edited`;
    await page.getByTestId("check-name-input").fill(updatedName);

    // Submit the edit form
    await page.getByTestId("check-submit-button").click();

    // Wait for navigation back to check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify updated name is shown
    await expect(page.getByRole("heading", { name: updatedName })).toBeVisible();

    // Verify the heartbeat endpoint is still visible (token preserved)
    await expect(page.getByText("Heartbeat Endpoint")).toBeVisible();
    await expect(page.getByText("Sample curl command:")).toBeVisible();

    // Verify the token URL is the same as before editing
    const heartbeatUrlAfter = await heartbeatUrlElement.textContent();
    expect(heartbeatUrlAfter).toContain("token=");
    expect(heartbeatUrlAfter).toBe(heartbeatUrlBefore);

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/checks-heartbeat-after-edit.png",
      fullPage: true,
    });
  });

  test("should persist adaptive resolution parameters after editing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Adaptive ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/adaptive-test");
    await page.getByTestId("check-submit-button").click();

    // Wait for check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Navigate to edit page
    await page.locator('a[href*="/edit"]').click();
    await page.waitForURL(/\/edit$/);
    await page.waitForLoadState("networkidle");

    // Set adaptive resolution parameters
    await page.locator("#reopenCooldownMultiplier").fill("3");
    await page.locator("#maxAdaptiveIncrease").fill("7");

    // Submit the edit form
    await page.getByTestId("check-submit-button").click();

    // Wait for navigation back to check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Navigate to edit page again to verify values were persisted
    await page.locator('a[href*="/edit"]').click();
    await page.waitForURL(/\/edit$/);
    await page.waitForLoadState("networkidle");

    // Verify the adaptive resolution values are still set
    await expect(page.locator("#reopenCooldownMultiplier")).toHaveValue("3");
    await expect(page.locator("#maxAdaptiveIncrease")).toHaveValue("7");

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/checks-adaptive-resolution-persisted.png",
      fullPage: true,
    });
  });

  test("should show validation error when URL is empty", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to new check form
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Fill in only the name, leave URL empty
    await page.getByTestId("check-name-input").fill("Test Check Without URL");

    // Submit the form
    await page.getByTestId("check-submit-button").click();

    // Wait a moment for validation
    await page.waitForTimeout(500);

    // Verify we're still on the form (didn't navigate away)
    expect(page.url()).toContain("/checks/new");

    // Check for error message
    await expect(page.getByText(/URL is required/i)).toBeVisible();

    // Take screenshot showing validation error
    await page.screenshot({
      path: "test-results/screenshots/checks-validation-error.png",
      fullPage: true,
    });
  });

  test("should create and then delete a check", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to new check form
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Generate a unique check name
    const checkName = `E2E Delete Test ${Date.now()}`;

    // Create the check
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com");
    await page.getByTestId("check-submit-button").click();

    // Wait for navigation to check detail page (UUID format, not /new)
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });

    // Find and click the delete button (trash icon)
    const deleteButton = page.locator('button:has([class*="lucide-trash"])');
    await deleteButton.click();

    // Confirm deletion in the alert dialog
    const confirmButton = page.getByRole("button", { name: "Delete" });
    await confirmButton.click();

    // Wait for navigation back to checks list
    await page.waitForURL(/\/checks$/, { timeout: 10000 });

    // Verify we're back on the checks list
    expect(page.url()).toMatch(/\/checks$/);

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/checks-after-delete.png",
      fullPage: true,
    });
  });
});
