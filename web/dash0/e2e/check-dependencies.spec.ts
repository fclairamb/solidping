import { test, expect, type Page } from "./fixtures";

// Dependencies are edited on the check EDIT page and only displayed on the
// check detail page (spec 2026-09-04-02). These tests pin both halves of that
// split, plus the two write paths that used to be broken:
//
//   - the edit page could only add and remove parents, so changing an existing
//     edge's kind or description was impossible (no `toUpdate` bucket);
//   - both the create and the edit page hard-coded `kind: "hard"`, so a soft
//     edge picked in the form was silently created as a hard one.
//
// The original regression coverage for issue #129 (a dependency row rendering
// as a bare kind badge once the check on the other end was deleted) is kept as
// the last test in the file.
test.describe("Check dependencies", () => {
  async function createCheck(page: Page, name: string): Promise<string> {
    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Checks" })
      .click();
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

  // Opens the Dependencies section of a check's edit form via the detail
  // page's "Edit" affordance, which deep-links `?section=dependencies`.
  async function openDependencyEditor(page: Page, detailUrl: string) {
    await page.goto(detailUrl);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("dependencies-edit-link").click();
    await page.waitForURL(/\/edit\?section=dependencies/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("dependency-add-row")).toBeVisible();
  }

  // Stages a parent in the form's add row and clicks "Add dependency".
  async function stageDependency(
    page: Page,
    parentName: string,
    kind: "Hard" | "Soft",
    description: string,
  ) {
    await page.getByRole("button", { name: "Pick a check…" }).click();
    await page.getByPlaceholder("Search checks…").fill(parentName);
    await page
      .locator('[data-testid^="check-picker-option-"]')
      .filter({ hasText: parentName })
      .first()
      .click();
    await page.getByTestId("dependency-add-kind-select").click();
    await page.getByRole("option", { name: kind, exact: true }).click();
    await page
      .getByTestId("dependency-add-description-input")
      .fill(description);
    await page.getByTestId("dependency-add-button").click();
  }

  async function saveCheck(page: Page) {
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(
      /\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
      { timeout: 15000 },
    );
    await page.waitForLoadState("networkidle");
  }

  test("a dependency is added from the edit page and shown read-only on the detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const stamp = Date.now();
    const parentName = `E2E Dep RO Parent ${stamp}`;
    const childName = `E2E Dep RO Child ${stamp}`;

    await createCheck(page, parentName);
    const childUrl = await createCheck(page, childName);

    await openDependencyEditor(page, childUrl);
    await stageDependency(page, parentName, "Hard", "shares the database");
    await saveCheck(page);

    // The detail page is a VIEW surface: link + kind badge + description,
    // and no mutation affordance anywhere in the Dependencies card.
    const dependsOn = page.getByTestId("depends-on-list");
    await expect(dependsOn.getByRole("link", { name: parentName })).toBeVisible();
    await expect(dependsOn.getByTestId("dependency-kind-hard")).toBeVisible();
    await expect(dependsOn.getByText("shares the database")).toBeVisible();
    // No inline editor, no per-row delete, no picker: the pencil, the trash
    // and the "Add dependency" button all moved to the edit page.
    await expect(
      dependsOn.getByRole("button", { name: "Add dependency" }),
    ).toHaveCount(0);
    await expect(
      dependsOn.getByRole("button", { name: "Pick a check…" }),
    ).toHaveCount(0);
    await expect(
      dependsOn.getByRole("button", { name: "Remove dependency" }),
    ).toHaveCount(0);
    await expect(dependsOn.locator("button")).toHaveCount(0);
  });

  test("changing an existing dependency's kind and description on the edit page is saved", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const stamp = Date.now();
    const parentName = `E2E Dep Update Parent ${stamp}`;
    const childName = `E2E Dep Update Child ${stamp}`;

    await createCheck(page, parentName);
    const childUrl = await createCheck(page, childName);

    // Start from a hard edge with no description.
    await openDependencyEditor(page, childUrl);
    await stageDependency(page, parentName, "Hard", "");
    await saveCheck(page);
    await expect(
      page.getByTestId("depends-on-list").getByTestId("dependency-kind-hard"),
    ).toBeVisible();

    // Re-open the editor: the row must come back carrying the edge's ACTUAL
    // kind (this is what the form used to drop), then retune it to soft with
    // a description. Both changes go through the `toUpdate` PATCH bucket —
    // without it, the save is a no-op and the assertions below fail.
    await openDependencyEditor(page, childUrl);
    const editorRow = page.locator('[data-testid^="dependency-editor-row-"]');
    await expect(editorRow).toHaveCount(1);
    await expect(editorRow).toContainText(parentName);
    const kindSelect = page.locator(
      '[data-testid^="dependency-kind-select-"]',
    );
    await expect(kindSelect).toHaveText("Hard");
    await kindSelect.click();
    await page.getByRole("option", { name: "Soft", exact: true }).click();
    await page
      .locator('[data-testid^="dependency-description-input-"]')
      .fill("informational only");
    await saveCheck(page);

    const dependsOn = page.getByTestId("depends-on-list");
    await expect(dependsOn.getByTestId("dependency-kind-soft")).toBeVisible();
    await expect(dependsOn.getByTestId("dependency-kind-hard")).toHaveCount(0);
    await expect(dependsOn.getByText("informational only")).toBeVisible();
  });

  test("a dependency added as soft is created soft, not hard", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const stamp = Date.now();
    const parentName = `E2E Dep Soft Parent ${stamp}`;
    const childName = `E2E Dep Soft Child ${stamp}`;

    await createCheck(page, parentName);
    const childUrl = await createCheck(page, childName);

    await openDependencyEditor(page, childUrl);
    await stageDependency(page, parentName, "Soft", "best effort upstream");
    await saveCheck(page);

    // The write path used to hard-code kind: "hard" — a soft pick landed as a
    // hard edge, so this assertion is the regression guard for it.
    const dependsOn = page.getByTestId("depends-on-list");
    await expect(dependsOn.getByRole("link", { name: parentName })).toBeVisible();
    await expect(dependsOn.getByTestId("dependency-kind-soft")).toBeVisible();
    await expect(dependsOn.getByTestId("dependency-kind-hard")).toHaveCount(0);
    await expect(dependsOn.getByText("best effort upstream")).toBeVisible();
  });

  // Regression coverage for issue #129: a dependency row used to render as a
  // bare kind badge with no check name attached, once the check on the other
  // end of the edge had been deleted (its check_dependencies row silently
  // outlived it — see server/internal/handlers/checkdependencies/service.go
  // and checks.Service.DeleteCheck).
  test("deleting a check clears it from the other check's dependencies card instead of leaving a bogus row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const stamp = Date.now();
    const parentName = `E2E Dep Parent ${stamp}`;
    const childName = `E2E Dep Child ${stamp}`;

    const parentUrl = await createCheck(page, parentName);
    const childUrl = await createCheck(page, childName);

    await openDependencyEditor(page, childUrl);
    await stageDependency(page, parentName, "Hard", "");
    await saveCheck(page);

    // Sanity: the edge shows up correctly on both sides before the delete.
    await expect(
      page.getByTestId("depends-on-list").getByRole("link", { name: parentName }),
    ).toBeVisible();

    await page.goto(parentUrl);
    await page.waitForLoadState("networkidle");
    await expect(
      page
        .getByTestId("depended-on-by-list")
        .getByRole("link", { name: childName }),
    ).toBeVisible();

    // Delete the parent check from its own detail page. The dependencies card
    // is read-only now, so the only trash-icon button on this page is the
    // check-delete one.
    await page.locator('button:has([class*="lucide-trash"])').click();
    await page.getByRole("button", { name: "Delete" }).click();
    await page.waitForURL(/\/checks$/, { timeout: 10000 });

    // Back on the child's detail page, the stale edge must be gone — no
    // bare kind badge with no name, just the normal empty state.
    await page.goto(childUrl);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(parentName)).toHaveCount(0);
    await expect(
      page.getByText(
        "No dependencies configured. Use Edit to add a parent and start cascading-incident rollup.",
      ),
    ).toBeVisible();
  });
});
