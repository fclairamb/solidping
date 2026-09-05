import { test, expect } from "./fixtures";
import { expandSection } from "./section-helpers";

// Regression test for spec 2026-07-15-04: editing a check's confirmation or
// recovery period silently failed to save because the edit route's onSubmit
// handler built the update PATCH payload by hand and omitted
// confirmationPeriodSeconds/recoveryPeriodSeconds. This test proves both
// fields round-trip through a real save + re-navigate, i.e. persisted
// server-side rather than merely held in client state.
test.describe("Check edit — confirmation/recovery period persistence", () => {
  test("editing confirmation and recovery period persists after save and reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check to edit.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Period Persist ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/period-persist-test");
    await page.getByTestId("check-submit-button").click();

    // Wait for check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Navigate to edit page
    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/edit$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Confirmation/recovery period live in the collapsed "incident tracking"
    // section by default.
    await expandSection(page, "section-incident-tracking-trigger");
    await page.getByTestId("confirmation-period-input").fill("240");
    await page.getByTestId("recovery-period-input").fill("180");

    // Submit the edit form
    await page.getByTestId("check-submit-button").click();

    // Wait for navigation back to check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Navigate to edit page again to verify the values were persisted
    // server-side (this bug's regression: a fresh fetch would show the old
    // defaults instead of the newly saved values).
    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/edit$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Non-default values auto-open the section; expand defensively regardless.
    await expandSection(page, "section-incident-tracking-trigger");
    await expect(page.getByTestId("confirmation-period-input")).toHaveValue("240");
    await expect(page.getByTestId("recovery-period-input")).toHaveValue("180");

    await page.screenshot({
      path: "test-results/screenshots/checks-period-persisted.png",
      fullPage: true,
    });
  });
});
