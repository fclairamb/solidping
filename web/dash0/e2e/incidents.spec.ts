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
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Incidents" }).click();

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
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Incidents" }).click();
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

  test("incident detail header: action buttons carry icons + aria-labels and back sits in the action group", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to incidents page via sidebar
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Incidents" }).click();
    await page.waitForURL(/\/incidents/);
    await page.waitForLoadState("networkidle");

    const hasIncidents = await waitForIncidentsLoaded(page);
    if (!hasIncidents) {
      test.skip(true, "No incidents available to open the detail page");
      return;
    }

    // Open the first incident's detail page
    const incidentLink = page.getByTestId("incident-row").first().getByRole("link").first();
    await incidentLink.click();
    await page.waitForURL(
      /\/incidents\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
      { timeout: 10000 },
    );
    await expect(page.getByText("Incident Details")).toBeVisible();

    // The back button is reachable via its aria-label (icon-only) and lives
    // in the right-hand action group alongside Refresh — not next to the title.
    const backButton = page.getByRole("button", { name: "Back to incidents" });
    await expect(backButton).toBeVisible();
    await expect(page.getByRole("button", { name: "Refresh" })).toBeVisible();

    // Desktop viewport: action labels are shown alongside the icon.
    await page.setViewportSize({ width: 1280, height: 800 });
    const resolveOnDesktop = page.getByRole("button", { name: "Resolve" });
    if (await resolveOnDesktop.count()) {
      await expect(resolveOnDesktop.first()).toContainText("Resolve");
    }

    // Mobile viewport: labels collapse, but the button is still reachable by
    // its aria-label (icon-only) — and there is no horizontal overflow.
    await page.setViewportSize({ width: 375, height: 812 });
    await expect(backButton).toBeVisible();
    const hasOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);

    await page.screenshot({
      path: "test-results/screenshots/incident-detail-actions-mobile.png",
      fullPage: true,
    });
  });

  test("should filter incidents by state", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to incidents page via sidebar
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Incidents" }).click();
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

  test("clicking a notification row opens the notification detail and back returns to the incident", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Deterministically seeded in test mode (server/test/testdata): an active
    // incident with one failed-webhook notification.
    const incidentUid = "00000000-0000-0000-0000-000000000013";

    await page.goto(`/dash0/orgs/test/incidents/${incidentUid}`);
    await page.waitForLoadState("networkidle");

    // We're on the incident detail page and the Notifications card is present.
    await expect(page.getByText("Incident Details")).toBeVisible();
    await expect(page.getByTestId("notifications-card")).toBeVisible();

    // At least one notification row is present; click the first one.
    const notifRow = page.getByTestId("notification-row").first();
    await expect(notifRow).toBeVisible();
    await notifRow.scrollIntoViewIfNeeded();
    await notifRow.click();

    // Clicking a notification row now opens the flat notification route with a
    // ?from=incident:<uid> source marker (not the legacy nested route).
    const notifUrlRe = new RegExp(
      `/orgs/test/notifications/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\\?from=incident%3A${incidentUid}`,
    );
    await page.waitForURL(notifUrlRe, { timeout: 10000 });

    // The notification detail page renders its content.
    await expect(
      page.getByRole("heading", { name: "Notification", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("Delivery timeline")).toBeVisible();

    // The back affordance returns to the incident detail page.
    await page.getByRole("button", { name: "Back to incident" }).click();
    await page.waitForURL(
      new RegExp(`/incidents/${incidentUid}$`),
      { timeout: 10000 },
    );
    await expect(page.getByText("Incident Details")).toBeVisible();
    await expect(page.getByTestId("notifications-card")).toBeVisible();
  });

  test("should navigate back from incident detail to list", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Navigate to incidents page via sidebar
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Incidents" }).click();
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

  test("direct URL navigation with showSuppressed=true activates the toggle", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Regression test for the validateSearch boolean-coercion bug: TanStack
    // Router already parses "?showSuppressed=true" into a native boolean
    // before validateSearch runs, so a bare `=== "true"` string comparison
    // there always evaluated to false and silently dropped the filter. This
    // only reproduces via a real URL round-trip (in-app clicks build the
    // search object directly), so navigate with page.goto rather than
    // driving the toggle through the UI.
    await page.goto("orgs/test/incidents?showSuppressed=true");
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { name: "Incidents" })).toBeVisible();
    await expect(page.getByTestId("incidents-show-suppressed-toggle")).toHaveAttribute(
      "data-state",
      "checked"
    );
  });

  test("direct URL navigation without showSuppressed leaves the toggle off", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/incidents");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("incidents-show-suppressed-toggle")).toHaveAttribute(
      "data-state",
      "unchecked"
    );
  });

  // The short `#42` reference is the same identifier a Telegram `/ack #42` and a
  // Slack alert header use. If the dashboard is the only surface that does NOT
  // show it, an on-call person cannot get from the page they are looking at to
  // the command they need to type.
  test("incidents carry their short #ref on the list and the detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/incidents");
    await page.waitForLoadState("networkidle");

    const hasIncidents = await waitForIncidentsLoaded(page);
    if (!hasIncidents) {
      test.skip(true, "No incidents available to check the reference on");
      return;
    }

    const firstRow = page.getByTestId("incident-row").first();
    const ref = firstRow.getByTestId("incident-number");
    await expect(ref).toBeVisible();

    const refText = ((await ref.textContent()) ?? "").trim();
    expect(refText).toMatch(/^#\d+$/);

    // The very same reference heads the detail page.
    await firstRow.getByRole("link").first().click();
    await page.waitForURL(
      /\/incidents\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
      { timeout: 10000 },
    );
    await expect(page.getByRole("heading", { level: 1 })).toContainText(refText);
  });
});
