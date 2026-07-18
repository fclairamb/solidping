import { test, expect } from "./fixtures";

test.describe("Incident comments", () => {
  // Deterministically seeded in test mode (server/test/testdata): an active
  // incident used by the notifications test too.
  const incidentUid = "00000000-0000-0000-0000-000000000013";

  test("add a comment via the composer and see it in the discussion", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${incidentUid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    // The Comments card renders with a composer.
    await expect(page.getByText("Comments", { exact: true })).toBeVisible();
    const composer = page.getByTestId("comment-input");
    await composer.scrollIntoViewIfNeeded();
    await expect(composer).toBeVisible();

    // Submit is disabled until there is text.
    const submit = page.getByTestId("comment-submit");
    await expect(submit).toBeDisabled();

    const commentText = `E2E comment ${Date.now()}`;
    await composer.fill(commentText);
    await expect(submit).toBeEnabled();
    await submit.click();

    // The new comment appears in the discussion list and the composer clears.
    await expect(
      page.getByTestId("incident-comment").filter({ hasText: commentText }),
    ).toBeVisible({ timeout: 10000 });
    await expect(composer).toHaveValue("");

    await page.screenshot({
      path: "test-results/screenshots/incident-comment-added.png",
      fullPage: true,
    });
  });

  test("comment composer is usable on a mobile viewport without overflow", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.goto(`/dash0/orgs/test/incidents/${incidentUid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Incident Details")).toBeVisible();

    const composer = page.getByTestId("comment-input");
    await composer.scrollIntoViewIfNeeded();
    await expect(composer).toBeVisible();

    // No horizontal overflow on the page body.
    const hasOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);
  });
});
