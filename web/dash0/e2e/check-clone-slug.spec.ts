import { test, expect } from "./fixtures";
import { expandSection } from "./section-helpers";

// Spec 2026-11-20-01: cloning a clone must not stack `-copy-copy`, the edit
// form can change the slug (with a one-click offer after a rename), and the
// inline slug editor selects its content.
test.describe("Check clone — slug", () => {
  test("clone of a clone is numbered, rename offers the slug, inline editor selects", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const base = `clonebase${Date.now()}`;

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.getByTestId("check-name-input").fill("Clone Base");
    await page.getByTestId("check-url-input").fill("https://example.com/clone-slug");
    await expandSection(page, "section-organization-trigger");
    await page.getByTestId("check-slug-input").fill(base);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });

    // First clone -> <base>-copy
    await page.getByTestId("check-more-actions").click();
    await page.getByTestId("check-clone-menu-item").click();
    await page.waitForURL(/\/edit$/, { timeout: 10000 });
    await expandSection(page, "section-organization-trigger");
    await expect(page.getByTestId("check-slug-input")).toHaveValue(`${base}-copy`);

    // Rename -> offer to update the slug from the name.
    await page.getByTestId("check-name-input").fill("Renamed Clone");
    await page.getByTestId("check-slug-from-name-offer").click();
    await expect(page.getByTestId("check-slug-input")).toHaveValue("renamed-clone");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f-]{27}(\?.*)?$/, { timeout: 10000 });

    // Inline editor: one click selects the slug, typing replaces it.
    await page.getByTestId("check-slug-edit-button").click();
    const input = page.getByTestId("check-slug-inline-input");
    await page.keyboard.type("fresh-slug");
    await expect(input).toHaveValue("fresh-slug");
  });
});
