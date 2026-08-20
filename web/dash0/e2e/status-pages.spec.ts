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

test.describe("Status pages list toolbar", () => {
  test("Refresh sits in the search toolbar row, and the header carries only New Status Page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Status Pages" }).click();
    await page.waitForURL(/\/status-pages$/);
    await page.waitForLoadState("networkidle");

    // Refresh (icon-only label hidden below sm, but its accessible name is
    // always "Refresh") and the New Status Page link both exist.
    const refreshButton = page.getByRole("button", { name: "Refresh" });
    await expect(refreshButton).toBeVisible();

    const newLink = page.getByRole("link", { name: "New Status Page" });
    await expect(newLink.first()).toBeVisible();

    // The PageHeader action cluster (`ml-auto flex shrink-0 items-center
    // gap-2`) carries only the primary "New Status Page" action now —
    // Refresh is a toolbar control, not a page-level action, so it moved out.
    const headerCluster = page.locator("div.ml-auto.flex.shrink-0.items-center.gap-2");
    await expect(headerCluster.getByRole("button", { name: "Refresh" })).toHaveCount(0);
    await expect(headerCluster.getByRole("link", { name: "New Status Page" })).toBeVisible();

    // Refresh sits in the same toolbar row as the search input, to its right.
    const searchInput = page.getByPlaceholder("Search status pages...");
    await expect(searchInput).toBeVisible();
    const toolbarRow = page
      .locator("div.flex.flex-wrap.items-center.gap-4")
      .filter({ has: refreshButton })
      .filter({ has: searchInput });
    await expect(toolbarRow).toHaveCount(1);
  });
});

test.describe("Status page availability thresholds", () => {
  test("thresholds round-trip: set, save, reload, values persist", async ({
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
    const name = `e2e-thresholds-${suffix}`;
    await page.locator("#name").fill(name);
    await page.locator("#slug").fill(`e2e-thresholds-${suffix}`.slice(0, 40));
    await page.getByRole("button", { name: "Create Status Page" }).click();

    // Not offered on the create form — only the edit form has these inputs.
    await expect(page.getByTestId("availability-thresholds")).toHaveCount(0);

    await page.waitForURL(/\/status-pages\/(?!new$)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const statusPageUrl = page.url();
    await page.goto(`${statusPageUrl}/edit`);
    await page.waitForLoadState("networkidle");

    // Empty by default (page thresholds unset ⇒ platform defaults).
    const thresholdsSection = page.getByTestId("availability-thresholds");
    await expect(thresholdsSection).toBeVisible();
    const upInput = page.locator("#thresholdUp");
    const degradedInput = page.locator("#thresholdDegraded");
    await expect(upInput).toHaveValue("");
    await expect(degradedInput).toHaveValue("");

    // Set custom thresholds and save.
    await upInput.fill("99.5");
    await degradedInput.fill("98");
    await page.getByRole("button", { name: "Save Changes" }).click();
    await page.waitForURL(/\/status-pages\/(?!new$)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Reload the edit page — the custom thresholds must persist.
    await page.goto(`${statusPageUrl}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator("#thresholdUp")).toHaveValue("99.5");
    await expect(page.locator("#thresholdDegraded")).toHaveValue("98");

    // Clearing both fields and saving resets to the platform defaults.
    await page.locator("#thresholdUp").fill("");
    await page.locator("#thresholdDegraded").fill("");
    await page.getByRole("button", { name: "Save Changes" }).click();
    await page.waitForURL(/\/status-pages\/(?!new$)[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    await page.goto(`${statusPageUrl}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator("#thresholdUp")).toHaveValue("");
    await expect(page.locator("#thresholdDegraded")).toHaveValue("");
  });
});
