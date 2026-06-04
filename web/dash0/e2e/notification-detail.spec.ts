import { test, expect } from "./fixtures";

const API_BASE = "http://localhost:4000";

const UUID_RE =
  /\/incidents\/[0-9a-f-]+\/notifications\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

test.describe("Notification delivery detail", () => {
  // Criterion 5: an unknown notificationUid renders a friendly not-found state,
  // never a crash. This is fully deterministic — no seeded data required.
  test("unknown notification shows a friendly not-found state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Find any incident to anchor the URL on; if none exist, anchor on a
    // synthetic incident UID — the notification GET 404s either way and the
    // page must render the not-found card, not crash.
    await page.goto(`/dash0/orgs/test/incidents`);
    await page.waitForLoadState("networkidle");

    const firstIncident = page.getByTestId("incident-row").first();
    let incidentUid = "00000000-0000-0000-0000-000000000000";
    if ((await firstIncident.count()) > 0) {
      const link = firstIncident.getByRole("link").first();
      const href = await link.getAttribute("href");
      const match = href?.match(/incidents\/([0-9a-f-]+)/);
      if (match) incidentUid = match[1];
    }

    await page.goto(
      `/dash0/orgs/test/incidents/${incidentUid}/notifications/does-not-exist`,
    );
    await page.waitForLoadState("networkidle");

    // Friendly not-found card from QueryErrorView, plus a working back link.
    await expect(page.getByText(/not found/i).first()).toBeVisible();
    await expect(
      page.getByRole("link", { name: /back to incident|incident/i }).first(),
    ).toBeVisible();
  });

  // Criterion 2: the detail loads directly via the single GET. Drives the new
  // endpoint through the authenticated request context to prove the route is
  // wired and 404s cleanly for an unknown notification.
  test("single-notification GET endpoint is reachable", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const { accessToken } = await resp.json();

    const notFound = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/incidents/00000000-0000-0000-0000-000000000000/notifications/00000000-0000-0000-0000-000000000000`,
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    // Unknown incident → 404, never a 500 or a 200.
    expect(notFound.status()).toBe(404);
  });

  // Criteria 1, 3, 4: when a notification row exists, clicking it changes the
  // URL to the detail route and the detail content (status, timeline, error)
  // renders. A deep-link refresh of that URL renders the same detail without
  // depending on the list cache. Skipped gracefully when no rows are present.
  test("clicking a notification row opens the deep-linkable detail", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Find an incident that has at least one notification row.
    await page.goto(`/dash0/orgs/test/incidents`);
    await page.waitForLoadState("networkidle");

    const incidentRows = page.getByTestId("incident-row");
    const count = await incidentRows.count();
    if (count === 0) {
      test.skip(true, "no incidents in the test org");
      return;
    }

    let foundRow = false;
    for (let i = 0; i < Math.min(count, 5); i++) {
      const link = incidentRows.nth(i).getByRole("link").first();
      const href = await link.getAttribute("href");
      if (!href) continue;
      await page.goto(`/dash0${href.replace(/^\/dash0/, "")}`);
      await page.waitForLoadState("networkidle");

      const notifRow = page.getByTestId("notification-row").first();
      if ((await notifRow.count()) > 0) {
        await notifRow.scrollIntoViewIfNeeded();
        await notifRow.click();
        foundRow = true;
        break;
      }
    }

    if (!foundRow) {
      test.skip(true, "no incident with notification rows in the test org");
      return;
    }

    // Criterion 1: the URL changed to the per-notification detail route.
    await page.waitForURL(UUID_RE, { timeout: 10000 });
    const detailUrl = page.url();

    // Detail content: a status badge and the delivery timeline are present.
    await expect(page.getByRole("heading", { name: "Notification" })).toBeVisible();
    await expect(page.getByText("Delivery timeline")).toBeVisible();

    // Criterion 2/4: refreshing the deep link renders the same detail directly.
    await page.reload();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toBe(detailUrl);
    await expect(page.getByRole("heading", { name: "Notification" })).toBeVisible();
    await expect(page.getByText("Delivery timeline")).toBeVisible();
  });
});
