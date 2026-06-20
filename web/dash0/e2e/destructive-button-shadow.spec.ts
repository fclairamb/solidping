import { test, expect } from "./fixtures";

/**
 * The destructive (delete) button mirrors the save button's tinted elevation:
 * a red-tinted drop shadow at rest that grows on hover (shadow-destructive ->
 * shadow-destructive-hover, wired in components/ui/button.tsx). This is a
 * visual/token change, so we assert the box-shadow is present at rest and
 * changes on hover rather than pixel-comparing exact color-mix values, which
 * resolve differently per browser.
 */
test.describe("Destructive button tinted elevation", () => {
  test("has a box-shadow at rest that changes on hover", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/design-reference");
    await page.waitForLoadState("networkidle");

    // The "Button variants" row renders the live destructive variant.
    const destructive = page
      .getByRole("button", { name: "Destructive", exact: true })
      .first();
    await destructive.waitFor({ state: "visible" });

    const restShadow = await destructive.evaluate(
      (el) => getComputedStyle(el).boxShadow,
    );
    // At-rest tint is wired (shadow-destructive), not a flat "none".
    expect(restShadow).not.toBe("none");
    expect(restShadow.trim()).not.toBe("");

    await destructive.hover();
    // Allow the box-shadow transition to settle.
    await page.waitForTimeout(300);

    const hoverShadow = await destructive.evaluate(
      (el) => getComputedStyle(el).boxShadow,
    );
    expect(hoverShadow).not.toBe("none");

    // The hover lift (shadow-destructive-hover) differs from the at-rest shadow.
    expect(hoverShadow).not.toBe(restShadow);
  });
});
