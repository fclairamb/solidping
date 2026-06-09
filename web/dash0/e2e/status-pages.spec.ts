import { test, expect } from "./fixtures";

test.describe("Status page detail header", () => {
  test("back arrow sits in the right action cluster, left of View, and navigates to the list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    // Create a fresh status page so we have a detail page to visit.
    await page.getByRole("link", { name: "New Status Page" }).first().click();
    await page.waitForURL(/\/status-pages\/new/);
    await page.waitForLoadState("networkidle");

    const suffix = Date.now().toString().slice(-9);
    const name = `e2e-back-arrow-${suffix}`;
    await page.locator("#name").fill(name);
    // Slug auto-derives from the name, but set it explicitly to be safe.
    await page.locator("#slug").fill(`e2e-back-${suffix}`.slice(0, 40));
    await page.getByRole("button", { name: "Create Status Page" }).click();

    // Lands on the detail route.
    await page.waitForURL(/\/status-pages\/[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name })).toBeVisible();

    // The back control still exists, icon-only with an accessible label.
    const backLink = page.getByRole("link", { name: "Back to status pages" });
    await expect(backLink).toBeVisible();

    const viewButton = page.getByRole("button", { name: "View" });
    await expect(viewButton).toBeVisible();

    // Back arrow and View share the same right-aligned action cluster
    // (the `flex gap-2 shrink-0` div), and the back arrow precedes View
    // in DOM order.
    const cluster = page.locator("div.flex.gap-2.shrink-0").filter({ has: backLink });
    await expect(cluster).toHaveCount(1);
    await expect(cluster.getByRole("button", { name: "View" })).toBeVisible();

    const orderedFirst = await backLink.evaluate((back, viewSelectorLabel) => {
      const cluster = back.closest("div.flex.gap-2.shrink-0");
      if (!cluster) return false;
      const view = Array.from(
        cluster.querySelectorAll("button"),
      ).find((b) => b.getAttribute("aria-label") === viewSelectorLabel);
      if (!view) return false;
      // back arrow must come before View in document order.
      return Boolean(
        back.compareDocumentPosition(view) & Node.DOCUMENT_POSITION_FOLLOWING,
      );
    }, "View");
    expect(orderedFirst).toBe(true);

    // The title group no longer contains the back control: the back arrow is
    // not a left-aligned sibling of the heading.
    const titleGroup = page
      .locator("div.min-w-0.flex-1")
      .filter({ has: page.getByRole("heading", { name }) });
    await expect(
      titleGroup.getByRole("link", { name: "Back to status pages" }),
    ).toHaveCount(0);

    // Clicking back navigates to the status pages list.
    await backLink.click();
    await page.waitForURL(/\/status-pages$/, { timeout: 10000 });
    await expect(page).toHaveURL(/\/dash0\/orgs\/[^/]+\/status-pages$/);
  });
});
