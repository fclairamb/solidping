import { test, expect } from "./fixtures";

// Deterministically seeded in test mode (server/test/testdata/testdata.go,
// createTestIncidentNotification): an active incident whose Details carry a
// first_result snapshot (spec 2026-08-13-11), giving this test a stable
// incident whose detail page always renders the failure-details panel.
const INCIDENT_UID = "00000000-0000-0000-0000-000000000013";

test.describe("Incident failure details", () => {
  test("incident detail page shows the first-failure panel", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${INCIDENT_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Incident Details")).toBeVisible();

    const card = page.getByTestId("failure-details-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText(
      "HTTP request failed: 503 Service Unavailable"
    );
    await expect(card).toContainText("First failure");

    // The captured output fields render as a compact key-value list.
    await expect(card).toContainText("status_code");
    await expect(card).toContainText("503");
  });

  test("first-failure panel is usable on mobile", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`/dash0/orgs/test/incidents/${INCIDENT_UID}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("failure-details-card");
    await expect(card).toBeVisible();

    const hasOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1
    );
    expect(hasOverflow).toBe(false);
  });
});
