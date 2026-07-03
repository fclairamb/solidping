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

test.describe("Status page history period (24h)", () => {
  test("the History Period select offers 24 hours and the choice round-trips", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a fresh status page.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages$/);
    await page.waitForLoadState("networkidle");

    await page.getByRole("link", { name: "New Status Page" }).first().click();
    await page.waitForURL(/\/status-pages\/new/);
    await page.waitForLoadState("networkidle");

    const suffix = Date.now().toString().slice(-9);
    const name = `e2e-24h-${suffix}`;
    await page.locator("#name").fill(name);
    await page.locator("#slug").fill(`e2e-24h-${suffix}`.slice(0, 40));
    await page.getByRole("button", { name: "Create Status Page" }).click();

    // Lands on the detail route; go to the edit page where the History Period
    // select lives. Wait for a real UID segment — `(?!new$)` excludes the
    // create route itself so we don't capture `.../status-pages/new` before
    // the POST has redirected (that would make `/edit` 404 "not found").
    await page.waitForURL(/\/status-pages\/(?!new$)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const statusPageUrl = page.url();
    await page.goto(`${statusPageUrl}/edit`);
    await page.waitForLoadState("networkidle");

    // The History Period select trigger defaults to "90 days".
    const periodSelect = page.locator("#historyPeriod");
    await expect(periodSelect).toBeVisible();
    await expect(periodSelect).toContainText("90 days");

    // Open it and pick "24 hours".
    await periodSelect.click();
    await page.getByRole("option", { name: "24 hours" }).click();
    await expect(periodSelect).toContainText("24 hours");

    // Save.
    await page.getByRole("button", { name: "Save Changes" }).click();
    await page.waitForURL(/\/status-pages\/(?!new$)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Reload the edit page — the 24h choice must persist.
    await page.goto(`${statusPageUrl}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator("#historyPeriod")).toContainText("24 hours");
  });
});

test.describe("Status pages list header", () => {
  test("Refresh sits in the header action cluster, left of New Status Page, and the empty state has no CTA", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages$/);
    await page.waitForLoadState("networkidle");

    // Both header actions exist: Refresh (icon-only label hidden below sm, but
    // its accessible name is always "Refresh") and the New Status Page link.
    const refreshButton = page.getByRole("button", { name: "Refresh" });
    await expect(refreshButton).toBeVisible();

    const newLink = page.getByRole("link", { name: "New Status Page" });
    await expect(newLink.first()).toBeVisible();

    // Refresh and New Status Page share the same right-aligned PageHeader action
    // cluster (`ml-auto flex shrink-0 items-center gap-2`), with Refresh first in
    // DOM order.
    const cluster = page
      .locator("div.ml-auto.flex.shrink-0.items-center.gap-2")
      .filter({ has: refreshButton });
    await expect(cluster).toHaveCount(1);
    await expect(cluster.getByRole("link", { name: "New Status Page" })).toBeVisible();

    const refreshFirst = await refreshButton.evaluate((refresh) => {
      const clusterEl = refresh.closest("div.ml-auto.flex.shrink-0.items-center.gap-2");
      if (!clusterEl) return false;
      const newAction = clusterEl.querySelector("a[href*='/status-pages/new']");
      if (!newAction) return false;
      // Refresh must come before New Status Page in document order.
      return Boolean(
        refresh.compareDocumentPosition(newAction) & Node.DOCUMENT_POSITION_FOLLOWING,
      );
    });
    expect(refreshFirst).toBe(true);

    // The redundant empty-state CTA is gone: there is no "Create your first
    // status page" button anywhere on the page (regardless of empty/non-empty).
    await expect(
      page.getByRole("button", { name: "Create your first status page" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Create your first status page" }),
    ).toHaveCount(0);
  });
});
