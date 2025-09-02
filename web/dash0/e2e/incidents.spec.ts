import { test, expect } from "./fixtures";

/**
 * Helper to wait for the incidents table to be in a settled state.
 * Returns true if incident rows are visible, false if empty.
 */
async function waitForIncidentsLoaded(page: import("@playwright/test").Page): Promise<boolean> {
  const incidentRow = page.getByTestId("incident-row").first();
  const noIncidents = page.getByText("No incidents found");

  // Wait for either incident rows or empty state to appear
  await Promise.race([
    incidentRow.waitFor({ state: "visible", timeout: 15000 }).catch(() => {}),
    noIncidents.waitFor({ state: "visible", timeout: 15000 }).catch(() => {}),
  ]);

  return incidentRow.isVisible();
}

test.describe("Incidents", () => {
  test("should display the incidents list page", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Click on Incidents in the sidebar to navigate
    await page.getByRole("link", { name: "Incidents" }).click();

    // Wait for navigation to complete
    await page.waitForURL(/\/incidents/);
    await page.waitForLoadState("networkidle");

    // Verify we're on the incidents page
    expect(page.url()).toContain("/incidents");

    // Check for page title
    await expect(page.getByRole("heading", { name: "Incidents" })).toBeVisible();

    // Check for state filter
    await expect(page.getByTestId("incidents-state-filter")).toBeVisible();

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/incidents-list.png",
      fullPage: true,
    });
  });

  test("should navigate to incident detail page when incidents exist", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to incidents page via sidebar
    await page.getByRole("link", { name: "Incidents" }).click();
    await page.waitForURL(/\/incidents/);
    await page.waitForLoadState("networkidle");

    // Wait for incidents to load
    const hasIncidents = await waitForIncidentsLoaded(page);

    if (hasIncidents) {
      // Click the incident title link in the first row
      const incidentLink = page.getByTestId("incident-row").first().getByRole("link").first();
      await incidentLink.click();

      // Wait for navigation to incident detail page (UUID format)
      await page.waitForURL(/\/incidents\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });

      // Verify we're on the incident detail page
      expect(page.url()).toMatch(/\/incidents\/[0-9a-f-]+$/);

      // Verify incident detail elements are visible (CardTitle is a div, not heading)
      await expect(page.getByText("Incident Details")).toBeVisible();
      await expect(page.getByText("Timeline")).toBeVisible();

      // Take screenshot of incident detail
      await page.screenshot({
        path: "test-results/screenshots/incident-detail.png",
        fullPage: true,
      });
    } else {
      // No incidents exist, verify empty state or error state is shown
      const noIncidents = page.getByText("No incidents found");
      const errorState = page.getByRole("button", { name: /retry/i });
      await expect(noIncidents.or(errorState)).toBeVisible({ timeout: 15000 });

      // Take screenshot of empty/error state
      await page.screenshot({
        path: "test-results/screenshots/incidents-empty.png",
        fullPage: true,
      });
    }
  });

  test("should filter incidents by state", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to incidents page via sidebar
    await page.getByRole("link", { name: "Incidents" }).click();
    await page.waitForURL(/\/incidents/);
    await page.waitForLoadState("networkidle");

    // Open state filter dropdown
    await page.getByTestId("incidents-state-filter").click();

    // Select "Active Only"
    await page.getByRole("option", { name: "Active Only" }).click();
    await page.waitForLoadState("networkidle");

    // Take screenshot of filtered view
    await page.screenshot({
      path: "test-results/screenshots/incidents-active-filter.png",
      fullPage: true,
    });

    // Open state filter dropdown again
    await page.getByTestId("incidents-state-filter").click();

    // Select "Resolved Only"
    await page.getByRole("option", { name: "Resolved Only" }).click();
    await page.waitForLoadState("networkidle");

    // Take screenshot of resolved filter
    await page.screenshot({
      path: "test-results/screenshots/incidents-resolved-filter.png",
      fullPage: true,
    });
  });

  test("should navigate back from incident detail to list", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to incidents page via sidebar
    await page.getByRole("link", { name: "Incidents" }).click();
    await page.waitForURL(/\/incidents/);
    await page.waitForLoadState("networkidle");

    // Wait for incidents to load
    const hasIncidents = await waitForIncidentsLoaded(page);

    if (hasIncidents) {
      // Click the incident title link in the first row
      const incidentLink = page.getByTestId("incident-row").first().getByRole("link").first();
      await incidentLink.click();

      // Wait for navigation to incident detail page
      await page.waitForURL(/\/incidents\/[0-9a-f-]+$/, { timeout: 10000 });

      // Navigate back to incidents list
      await page.goBack();

      // Wait for navigation back to incidents list
      await page.waitForURL(/\/incidents/, { timeout: 10000 });

      // Verify we're back on the incidents list
      await expect(page.getByRole("heading", { name: "Incidents" })).toBeVisible();

      // Take screenshot after navigating back
      await page.screenshot({
        path: "test-results/screenshots/incidents-back-to-list.png",
        fullPage: true,
      });
    } else {
      // No incidents, take screenshot of empty state
      await page.screenshot({
        path: "test-results/screenshots/incidents-no-back-nav.png",
        fullPage: true,
      });
    }
  });
});
