import { test, expect } from "./fixtures";

// Spec 2026-11-17-04: the JS check editor's "Test" button runs the UNSAVED
// script once and shows the step list inline, with the failed step and the
// skipped one after it.
const SCRIPT =
  `step("Open", function () {}); ` +
  `try { step("Click Se connecter", function () { throw new Error("no such element"); }); } catch (e) {} ` +
  `step("Check welcome", function () {}); ` +
  `return { status: "up" };`;

test.describe("JS check — test run with steps", () => {
  test("Test shows ok / failed / skipped steps without saving", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=js");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await page.getByTestId("check-script").locator(".cm-content").fill(SCRIPT);

    await page.getByTestId("js-test-run-button").click();

    const items = page.getByTestId("check-step-item");
    await expect(items).toHaveCount(3);
    await expect(items.nth(0)).toHaveAttribute("data-status", "ok");
    await expect(items.nth(1)).toHaveAttribute("data-status", "failed");
    await expect(items.nth(2)).toHaveAttribute("data-status", "skipped");
    await expect(page.getByTestId("check-step-error")).toContainText("no such element");
    await expect(page.getByTestId("check-steps-list")).toBeVisible();
  });
});
