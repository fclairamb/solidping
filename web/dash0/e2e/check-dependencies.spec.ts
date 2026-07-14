import { test, expect } from "./fixtures";

// Regression coverage for issue #129: a dependency row used to render as a
// bare kind badge with no check name attached, once the check on the other
// end of the edge had been deleted (its check_dependencies row silently
// outlived it — see server/internal/handlers/checkdependencies/service.go
// and checks.Service.DeleteCheck). This drives the real UI end to end: link
// two checks with a dependency, delete the parent, and confirm the child's
// dependencies card falls back to the empty state instead of a bogus row.
test.describe("Check dependencies", () => {
  test("deleting a check clears it from the other check's dependencies card instead of leaving a bogus row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const stamp = Date.now();
    const parentName = `E2E Dep Parent ${stamp}`;
    const childName = `E2E Dep Child ${stamp}`;

    async function createCheck(name: string): Promise<string> {
      await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
      await page.waitForURL(/\/checks$/);
      await page.waitForLoadState("networkidle");
      await page.getByTestId("new-check-button").click();
      await page.waitForURL(/\/checks\/new/);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("check-name-input")).toBeVisible();
      await page.getByTestId("check-name-input").fill(name);
      await page
        .getByTestId("check-url-input")
        .fill(`https://example.com/${encodeURIComponent(name)}`);
      await page.getByTestId("check-submit-button").click();
      await page.waitForURL(
        /\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
        { timeout: 10000 },
      );
      await page.waitForLoadState("networkidle");
      await expect(page.getByRole("heading", { name })).toBeVisible();
      return page.url();
    }

    const parentUrl = await createCheck(parentName);
    const childUrl = await createCheck(childName);

    // On the child's detail page, add the parent as a dependency ("child
    // depends on parent") via the Dependencies card's picker.
    await page.goto(childUrl);
    await page.waitForLoadState("networkidle");
    await page.getByRole("button", { name: "Pick a check…" }).click();
    await page.getByPlaceholder("Search checks…").fill(parentName);
    await page
      .locator('[data-testid^="check-picker-option-"]')
      .filter({ hasText: parentName })
      .first()
      .click();
    await page.getByRole("button", { name: "Add dependency" }).click();

    // Sanity: the edge shows up correctly on both sides before the delete.
    // Scope via the check-name link (only the real edge row renders one —
    // the "Add dependency" picker row below it shares the same
    // `.rounded-md.border` styling but has no link), so this can't
    // accidentally match the add-row before the mutation settles.
    const parentLink = page.getByRole("link", { name: parentName });
    await expect(parentLink).toBeVisible();
    const dependsOnRow = page.locator("div.rounded-md.border", { has: parentLink });
    await expect(dependsOnRow.getByText("Hard")).toBeVisible();

    await page.goto(parentUrl);
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("link", { name: childName })).toBeVisible();

    // Delete the parent check from its own detail page. It has no other
    // trash-icon buttons on this page (the "Depended on by" row has none),
    // so this unambiguously targets the check-delete button.
    await page.locator('button:has([class*="lucide-trash"])').click();
    await page.getByRole("button", { name: "Delete" }).click();
    await page.waitForURL(/\/checks$/, { timeout: 10000 });

    // Back on the child's detail page, the stale edge must be gone — no
    // bare "Hard" badge with no name, just the normal empty state.
    await page.goto(childUrl);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(parentName)).toHaveCount(0);
    await expect(
      page.getByText(
        "No dependencies configured. Add a parent above to start cascading-incident rollup.",
      ),
    ).toBeVisible();
  });
});
