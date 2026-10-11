import { test, expect } from "./fixtures";

/**
 * A modal that was just dismissed must release the page immediately.
 *
 * Radix holds a dismissed dialog in the DOM until its exit animation fires
 * `animationend`, and the DismissableLayer that closed it stays live for that
 * whole window. During it the `fixed inset-0` overlay is still the hit-test
 * target for the entire page, and re-opening the dialog is swallowed by the
 * stale layer. Nothing on screen says so — the modal looks gone.
 *
 * `organization-parameters.spec.ts` hit this for real: save a parameter, then
 * click Rotate on the row that just appeared (the list refetch lands at the
 * same instant the dialog closes), and ~50% of local runs lost the click.
 *
 * The fix is that dialog/alert-dialog/sheet no longer animate OUT, so
 * `Presence` unmounts them synchronously and there is no window to land in.
 *
 * Two details here are load-bearing, and removing either makes this test pass
 * against the bug it exists to catch:
 *
 *  - the re-open click follows `Escape` with no wait in between. Waiting for
 *    the dialog to be hidden waits out the exit animation, which is the whole
 *    problem.
 *  - the click is `force: true`. Playwright would otherwise notice the overlay
 *    intercepting, retry until it unmounted, and report a pass — which is
 *    exactly why this only ever showed up as an intermittent failure elsewhere.
 *    A real user's click is not retried.
 */
test.describe("Dismissed dialog", () => {
  test("releases the page as soon as it is dismissed", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/parameters");
    await page.waitForLoadState("networkidle");

    const trigger = page.getByTestId("parameter-add");
    const keyInput = page.getByTestId("parameter-key-input");

    await trigger.click();
    await expect(keyInput).toBeVisible();

    await page.keyboard.press("Escape");

    // Synchronize on the dialog no longer being OPEN — not on it being gone.
    // Waiting for it to disappear would wait out the exit animation, which is
    // the window under test; not waiting at all would click while it is still
    // legitimately open.
    await expect
      .poll(() =>
        page.evaluate(
          () => document.querySelectorAll('[role="dialog"][data-state="open"]').length,
        ),
      )
      .toBe(0);

    await trigger.click({ force: true });

    // Let any teardown finish before judging the result: with a lingering
    // overlay the click above landed on it, and the dialog that is still
    // visible right now is the old one on its way out, not a re-opened one.
    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              // Fixed-position only: plenty of unrelated Radix bits (accordions,
              // tooltips, menu triggers) sit at data-state="closed" all the time.
              Array.from(document.querySelectorAll('[data-state="closed"]')).filter(
                (node) => getComputedStyle(node).position === "fixed",
              ).length,
          ),
        { message: "a dismissed modal must not linger over the page" },
      )
      .toBe(0);
    await expect(keyInput, "the re-open click must not be swallowed").toBeVisible();

    // And the page underneath is the pointer target again the moment it closes.
    await page.keyboard.press("Escape");
    const hitsTrigger = await trigger.evaluate((el) => {
      const rect = el.getBoundingClientRect();
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
      return hit ? hit === el || el.contains(hit) : false;
    });
    expect(hitsTrigger, "the page underneath must be clickable again").toBe(true);
  });
});
