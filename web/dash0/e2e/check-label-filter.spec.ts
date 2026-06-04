import { test, expect, type Page } from "./fixtures";

// Exercises the faceted label filter in the checks-list toolbar
// (web/dash0/src/components/shared/label-filter.tsx). Uses the typed-key /
// typed-value path so the tests are independent of seeded label data, plus a
// deep-link assertion that the URL contract renders chips on load.

test.describe("Checks label filter", () => {
  async function gotoChecks(page: Page) {
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("label-filter-trigger")).toBeVisible();
  }

  // addFilter drives the two-step popover with typed values, which always shows
  // a "Use …" item regardless of whether suggestions exist.
  async function addFilter(page: Page, key: string, value: string) {
    await page.getByTestId("label-filter-trigger").click();

    const keyInput = page.getByTestId("label-filter-key-input");
    await keyInput.waitFor({ state: "visible" });
    await keyInput.fill(key);
    await page.getByTestId("label-filter-key-use-typed").click();

    const valueInput = page.getByTestId("label-filter-value-input");
    await valueInput.waitFor({ state: "visible" });
    await valueInput.fill(value);
    await page.getByTestId("label-filter-value-use-typed").click();
  }

  test("pick a key and value adds a chip and updates the URL", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoChecks(page);

    await addFilter(page, "env", "prod");

    // Close the popover so it does not overlay assertions.
    await page.keyboard.press("Escape");

    await expect(page.getByTestId("label-chips")).toContainText("env: prod");
    await expect.poll(() => new URL(page.url()).searchParams.get("labels")).toBe("env:prod");
  });

  test("stacking a second filter comma-separates the URL param", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoChecks(page);

    await addFilter(page, "env", "prod");
    // The popover resets to step 1 and stays open — add the second filter.
    const keyInput = page.getByTestId("label-filter-key-input");
    await keyInput.waitFor({ state: "visible" });
    await keyInput.fill("team");
    await page.getByTestId("label-filter-key-use-typed").click();
    const valueInput = page.getByTestId("label-filter-value-input");
    await valueInput.waitFor({ state: "visible" });
    await valueInput.fill("ops");
    await page.getByTestId("label-filter-value-use-typed").click();

    await page.keyboard.press("Escape");

    await expect(page.getByTestId("label-chips")).toContainText("env: prod");
    await expect(page.getByTestId("label-chips")).toContainText("team: ops");
    // serializeLabelsParam sorts keys alphabetically: env before team.
    await expect.poll(() => new URL(page.url()).searchParams.get("labels")).toBe(
      "env:prod,team:ops",
    );
  });

  test("removing a chip updates the URL", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoChecks(page);

    await addFilter(page, "env", "prod");
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("label-chips")).toContainText("env: prod");

    await page.getByTestId("label-chip-remove-env").click();
    await expect(page.getByTestId("label-chips")).toHaveCount(0);
    await expect.poll(() => new URL(page.url()).searchParams.get("labels")).toBeNull();
  });

  test("clear filters empties the labels param and removes chips", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoChecks(page);

    await addFilter(page, "env", "prod");
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("label-chips")).toContainText("env: prod");

    await page.getByTestId("clear-label-filters").click();
    await expect(page.getByTestId("label-chips")).toHaveCount(0);
    await expect.poll(() => new URL(page.url()).searchParams.get("labels")).toBeNull();
  });

  test("deep-linking ?labels renders the chip on load", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoChecks(page);

    // Navigate directly with the labels param set.
    const target = new URL(page.url());
    target.searchParams.set("labels", "env:prod");
    await page.goto(target.toString());
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("label-chips")).toContainText("env: prod");
    await expect(page.getByTestId("clear-label-filters")).toBeVisible();
  });
});
