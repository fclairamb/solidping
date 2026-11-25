import { test, expect } from "./fixtures";

// Issue #1036: a check's description (set through the API or MCP) had no field
// in the check form, so the Web UI could neither show nor change it.
test.describe("Check description", () => {
  test("can be set on create, edited, and cleared from the form", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.getByTestId("check-name-input").fill(`Described ${Date.now()}`);
    await page.getByTestId("check-url-input").fill("https://example.com/description");
    await page.getByTestId("check-description-input").fill("Set at creation");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f-]{27}(\?.*)?$/, { timeout: 10000 });
    const detailUrl = page.url();
    await expect(page.getByTestId("check-detail-description")).toHaveText("Set at creation");

    // Edit: the stored description is pre-filled and can be changed.
    await page.goto(detailUrl.replace(/(\?.*)?$/, "/edit"));
    const field = page.getByTestId("check-description-input");
    await expect(field).toHaveValue("Set at creation");
    await field.fill("Edited from the UI");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f-]{27}(\?.*)?$/, { timeout: 10000 });
    await expect(page.getByTestId("check-detail-description")).toHaveText("Edited from the UI");

    // Clearing the field clears the description.
    await page.goto(detailUrl.replace(/(\?.*)?$/, "/edit"));
    await expect(page.getByTestId("check-description-input")).toHaveValue("Edited from the UI");
    await page.getByTestId("check-description-input").fill("");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f-]{27}(\?.*)?$/, { timeout: 10000 });
    await expect(page.getByTestId("check-name-input")).toHaveCount(0);
    await expect(page.getByTestId("check-detail-description")).toHaveCount(0);
    await page.goto(detailUrl.replace(/(\?.*)?$/, "/edit"));
    await expect(page.getByTestId("check-name-input")).not.toHaveValue("");
    await expect(page.getByTestId("check-description-input")).toHaveValue("");
  });
});
