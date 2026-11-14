import { test, expect, stubSavedPreferences } from "./fixtures";
import type { Route } from "@playwright/test";

// The checks list header on a phone: its actions (new group, new check,
// "describe it", the "…" menu) have their own row below the title. The "…"
// menu used to wrap alone onto a second row, at its LEFT edge, on a 361 px
// wide phone in French with AI enabled (the longest row). It must stay at the
// right end of the actions.

test.describe("Checks list header (mobile)", () => {
  test("the more-actions menu sits at the right end of the actions row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 361, height: 654 });
    // French: its "Nouveau contrôle" is among the widest primary labels. The
    // account's saved language wins over the browser's, so stub that one.
    await stubSavedPreferences(page, () => ({ locale: "fr" }));
    // AI on: the "describe it" button joins the row, as on an AI-enabled server.
    await page.route("**/api/v1/config", async (route: Route) => {
      const response = await route.fetch();
      const body = await response.json();
      await route.fulfill({ response, json: { ...body, ai: { enabled: true } } });
    });

    await page.goto("orgs/test/checks");

    const newCheck = page.getByTestId("new-check-button");
    const describe = page.getByTestId("describe-check-button");
    const more = page.getByTestId("checks-more-actions");
    await expect(newCheck).toHaveText(/Nouveau contrôle/);
    await expect(describe).toBeVisible();
    await expect(more).toBeVisible();

    const newCheckBox = await newCheck.boundingBox();
    const describeBox = await describe.boundingBox();
    const moreBox = await more.boundingBox();
    expect(newCheckBox && describeBox && moreBox).toBeTruthy();
    if (!newCheckBox || !describeBox || !moreBox) return;

    // One row: the menu is level with the primary action, after "describe it".
    expect(Math.abs(moreBox.y - newCheckBox.y)).toBeLessThan(2);
    expect(moreBox.x).toBeGreaterThan(describeBox.x + describeBox.width);

    // And nothing in the header pushes the page sideways.
    const overflows = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(overflows).toBe(false);
  });
});
