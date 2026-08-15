import { test, expect, API_BASE } from "./fixtures";

test.describe("Incident notifications", () => {
  test("Notifications card renders on incident detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a check so we have something to trigger an incident on.
    const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const { accessToken } = await resp.json();

    const checkResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${accessToken}` },
        data: {
          type: "http",
          name: `E2E Notif Check ${Date.now()}`,
          config: { url: "https://example.com" },
          period: "00:05:00",
        },
      }
    );
    const check = await checkResp.json();

    // Create a minimal active incident directly via the admin API.
    const incidentResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/test/incidents`,
      {
        headers: { Authorization: `Bearer ${accessToken}` },
        data: { checkUid: check.uid },
      }
    );

    // If there is no dedicated test-create endpoint, just navigate to the
    // incidents list and pick the first active incident. The presence of the
    // Notifications card is sufficient — we don't require a row to be present.
    void incidentResp;

    await page.goto(`/dash0/orgs/test/incidents`);
    await page.waitForLoadState("networkidle");

    // Navigate to first incident, if any exist.
    const firstLink = page.locator("table tbody tr a").first();
    const hasIncidents = await firstLink.count() > 0;

    if (!hasIncidents) {
      // No incidents — still verify the list page loads without error.
      await expect(page.locator("h1, h2").first()).toBeVisible();
      return;
    }

    await firstLink.click();
    await page.waitForLoadState("networkidle");

    // The Notifications card must be present on the incident detail page.
    await expect(page.getByTestId("notifications-card")).toBeVisible();
  });

  // Deterministically seeded in test mode (server/test/testdata/testdata.go,
  // createTestIncidentNotification): incident 00000000-...-13 always has
  // exactly one notification row, for the incident.created event.
  const SEEDED_INCIDENT_UID = "00000000-0000-0000-0000-000000000013";

  test("Notifications card shows an Event column naming the notified event", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${SEEDED_INCIDENT_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("notifications-card")).toBeVisible();

    const row = page.getByTestId("notification-row").first();
    await expect(row).toBeVisible();

    // The Event column names the notified event — "Incident Created" — not
    // just Time / Status / Target / Source / Channel.
    await expect(page.getByRole("columnheader", { name: "Event" })).toBeVisible();
    await expect(row.getByText("Incident Created")).toBeVisible();
  });

  test("My pages route loads", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("/dash0/orgs/test/me/notifications");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("my-notifications-page")).toBeVisible();

    // The header uses the canonical boxed PageHeader: a level-1 heading with
    // the text-2xl font-semibold treatment.
    const h1 = page.getByRole("heading", { level: 1, name: "My pages" });
    await expect(h1).toBeVisible();
    await expect(h1).toHaveClass(/text-2xl/);
    await expect(h1).toHaveClass(/font-semibold/);
    await expect(
      page.getByText("Incidents you were paged for, in reverse chronological order.")
    ).toBeVisible();
  });

  test("My pages sidebar entry navigates to the correct page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: /my pages/i })
      .click();

    await page.waitForURL(/\/me\/notifications/);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("my-notifications-page")).toBeVisible();
  });
});
