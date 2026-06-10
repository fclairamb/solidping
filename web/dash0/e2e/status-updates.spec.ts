import { test, expect } from "./fixtures";

test.describe("Status Updates", () => {
  test("should display the list page and create, edit, then delete an update", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate via sidebar
    await page.getByRole("link", { name: "Status updates" }).click();
    await page.waitForURL(/\/status-updates/);
    await page.waitForLoadState("networkidle");

    // Verify we're on the list page
    expect(page.url()).toContain("/status-updates");
    await expect(page.getByRole("heading", { name: "Status updates" })).toBeVisible();

    // Both filter selects must render — a regression in Radix SelectItem value="" crashes before this
    await expect(page.getByTestId("status-updates-page-filter")).toBeVisible();
    await expect(page.getByTestId("status-updates-kind-filter")).toBeVisible();

    await page.screenshot({
      path: "test-results/screenshots/status-updates-list.png",
      fullPage: true,
    });

    // The "New update" button shows its label at all widths (aligned with the
    // status-pages "New status page" button) — not hidden on mobile.
    const newUpdateButton = page.getByTestId("status-updates-new");
    await expect(newUpdateButton).toBeVisible();
    await expect(newUpdateButton).toContainText("New update");

    // Navigate to create page
    await newUpdateButton.click();
    await page.waitForURL(/\/status-updates\/new/);
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { name: "New status update" })).toBeVisible();

    await page.screenshot({
      path: "test-results/screenshots/status-updates-new.png",
      fullPage: true,
    });

    // Fill in the form
    const uniqueTitle = `E2E test update ${Date.now()}`;
    await page.getByTestId("status-update-form-title").fill(uniqueTitle);
    await page.getByTestId("status-update-form-body").fill("This is an automated test update.");

    // Submit
    await page.getByTestId("status-update-form-submit").click();

    // Should navigate back to the list
    await page.waitForURL(/\/status-updates\/?$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // The new row should be visible
    await expect(page.getByText(uniqueTitle)).toBeVisible({ timeout: 10000 });

    await page.screenshot({
      path: "test-results/screenshots/status-updates-after-create.png",
      fullPage: true,
    });

    // Click the edit (Pencil) button on the new row
    const newRow = page.getByTestId("status-update-row").filter({ hasText: uniqueTitle });
    await newRow.getByTestId("status-update-row-edit").click();
    await page.waitForURL(
      /\/status-updates\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\/edit/,
      { timeout: 10000 }
    );
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { name: "Edit status update" })).toBeVisible();
    // Title should be pre-filled
    await expect(page.getByTestId("status-update-form-title")).toHaveValue(uniqueTitle);

    await page.screenshot({
      path: "test-results/screenshots/status-updates-edit.png",
      fullPage: true,
    });

    // Save without changes
    await page.getByTestId("status-update-form-submit").click();
    await page.waitForURL(/\/status-updates\/?$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    await page.screenshot({
      path: "test-results/screenshots/status-updates-after-edit.png",
      fullPage: true,
    });

    // Delete the row we created
    const rowToDelete = page.getByTestId("status-update-row").filter({ hasText: uniqueTitle });
    await rowToDelete.getByTestId("status-update-row-delete").click();

    // Confirm in the AlertDialog
    await page.getByRole("button", { name: "Delete" }).click();
    await page.waitForLoadState("networkidle");

    // Row should be gone
    await expect(page.getByText(uniqueTitle)).not.toBeVisible({ timeout: 10000 });

    await page.screenshot({
      path: "test-results/screenshots/status-updates-after-delete.png",
      fullPage: true,
    });
  });
});
