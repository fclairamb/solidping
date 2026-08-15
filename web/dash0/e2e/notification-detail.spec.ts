import { test, expect, API_BASE } from "./fixtures";

// The new flat notification URL: /orgs/:org/notifications/:uuid (with optional ?from=...)
const FLAT_NOTIF_RE =
  /\/notifications\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/;

test.describe("Notification delivery detail", () => {
  // Criterion 5: an unknown notificationUid renders a friendly not-found state,
  // never a crash. This is fully deterministic — no seeded data required.
  test("unknown notification shows a friendly not-found state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // The new flat route: /orgs/:org/notifications/:uid
    await page.goto(
      `/dash0/orgs/test/notifications/does-not-exist`,
    );
    await page.waitForLoadState("networkidle");

    // Friendly not-found card from QueryErrorView.
    await expect(page.getByText(/not found/i).first()).toBeVisible();
  });

  // Verifies the org-level single-notification GET endpoint is wired correctly.
  test("single-notification GET endpoint (org-level) is reachable", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const { accessToken } = await resp.json();

    // Unknown notification → 404, never a 500 or a 200.
    const notFound = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/notifications/00000000-0000-0000-0000-000000000000`,
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    expect(notFound.status()).toBe(404);
  });

  // Verifies the list-by-org endpoint requires connectionUid.
  test("org-level notification list requires connectionUid", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const { accessToken } = await resp.json();

    const bad = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/notifications`,
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    expect(bad.status()).toBe(400);
  });

  // Criteria 1, 3, 4: when a notification row exists, clicking it changes the
  // URL to the flat notification detail route with ?from=incident:... and the
  // detail content (status, timeline) renders. A deep-link refresh renders the
  // same detail. Skipped gracefully when no rows are present.
  test("clicking a notification row opens the flat detail route with ?from=", async ({
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

    // Collect incident hrefs while still on the list (navigating away inside the
    // loop would detach the incident-row locators).
    const hrefs: string[] = [];
    for (let i = 0; i < Math.min(count, 5); i++) {
      const href = await incidentRows
        .nth(i)
        .getByRole("link")
        .first()
        .getAttribute("href");
      if (href) hrefs.push(href);
    }

    let foundRow = false;
    for (const href of hrefs) {
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

    // The URL changes to the flat notification route (not the old nested route).
    await page.waitForURL(/[?&]from=incident/, { timeout: 10000 });
    const detailUrl = page.url();

    // The URL must contain ?from=incident:... (the colon is %-encoded in the URL).
    expect(decodeURIComponent(detailUrl)).toMatch(/\?from=incident:/);
    // And use the new flat path (no /incidents/ segment before /notifications/).
    expect(detailUrl).toMatch(/\/orgs\/test\/notifications\//);

    // Detail content: a status badge and the delivery timeline are present.
    await expect(page.getByRole("heading", { name: "Notification" })).toBeVisible();
    await expect(page.getByText("Delivery timeline")).toBeVisible();

    // Refreshing the deep link renders the same detail directly.
    await page.reload();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toBe(detailUrl);
    await expect(page.getByRole("heading", { name: "Notification" })).toBeVisible();
    await expect(page.getByText("Delivery timeline")).toBeVisible();
  });

  // Deterministically seeded in test mode (server/test/testdata/testdata.go,
  // createTestIncidentNotification): a fixed incident.created notification.
  const SEEDED_INCIDENT_UID = "00000000-0000-0000-0000-000000000013";
  const SEEDED_NOTIF_UID = "00000000-0000-0000-0000-000000000014";

  // Event-type identity (spec 2026-08-15-02): the bare `<code>{eventType}</code>`
  // is gone, replaced by the same EventTypeBadge (emoji + translated label)
  // used everywhere else an event type renders.
  test("shows the labeled event badge instead of the bare event-type code", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(
      `/dash0/orgs/test/notifications/${SEEDED_NOTIF_UID}?from=incident:${SEEDED_INCIDENT_UID}`,
    );
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { name: "Notification" })).toBeVisible();

    // The translated label is visible...
    await expect(page.getByText("Incident Created")).toBeVisible();
    // ...and the raw event-type string is no longer rendered as a bare <code>
    // element (it still lives in the badge's title attribute for debugging).
    await expect(page.locator("code", { hasText: "incident.created" })).toHaveCount(0);
  });

  // Legacy redirect: the old nested URL forwards to the flat route.
  test("old nested notification URL redirects to flat route", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const incidentUid = "11111111-1111-1111-1111-111111111111";
    const notifUid = "22222222-2222-2222-2222-222222222222";

    // Intercept both old and new endpoints so the page can render.
    await page.route(
      `**/api/v1/orgs/test/incidents/${incidentUid}/notifications/${notifUid}`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: notifUid,
            incidentUid,
            eventType: "incident.created",
            source: "check_connection",
            channelType: "webhook",
            status: "sent",
            createdAt: "2026-06-02T10:00:00Z",
            sentAt: "2026-06-02T10:00:02Z",
            user: null,
            connection: { uid: "c-1", name: "Prod hook", type: "webhook" },
          }),
        });
      },
    );
    await page.route(
      `**/api/v1/orgs/test/notifications/${notifUid}`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: notifUid,
            incidentUid,
            eventType: "incident.created",
            source: "check_connection",
            channelType: "webhook",
            status: "sent",
            createdAt: "2026-06-02T10:00:00Z",
            sentAt: "2026-06-02T10:00:02Z",
            user: null,
            connection: { uid: "c-1", name: "Prod hook", type: "webhook" },
          }),
        });
      },
    );

    await page.goto(
      `/dash0/orgs/test/incidents/${incidentUid}/notifications/${notifUid}`,
    );
    // The redirect fires: URL should now be the flat route.
    await page.waitForURL(/[?&]from=incident/, { timeout: 10000 });
    const decoded = decodeURIComponent(page.url());
    expect(decoded).toContain(`/notifications/${notifUid}`);
    expect(decoded).toContain(`?from=incident:${incidentUid}`);
  });

  // Capture-notification-delivery-artifacts criteria 1/2/3: a failed webhook
  // surfaces the Delivery section with a status-code badge, the duration, the
  // stripped request URL, and copyable request/response bodies. The notification
  // GET is mocked so the assertion is deterministic.
  test("failed webhook renders the Delivery section with status, bodies and url", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

    const notifUid = "22222222-2222-2222-2222-222222222222";

    // Mock the new org-level endpoint.
    await page.route(
      `**/api/v1/orgs/test/notifications/${notifUid}`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: notifUid,
            incidentUid: "11111111-1111-1111-1111-111111111111",
            eventType: "incident.created",
            source: "check_connection",
            channelType: "webhook",
            status: "failed",
            error: "webhook request failed: status 503",
            createdAt: "2026-06-02T10:00:00Z",
            failedAt: "2026-06-02T10:00:01Z",
            jobUid: "33333333-3333-3333-3333-333333333333",
            user: null,
            connection: { uid: "c-1", name: "Prod hook", type: "webhook" },
            deliveryDetails: {
              httpStatusCode: 503,
              requestUrl: "https://hooks.example.com/incidents",
              requestBody: '{"type":"incident.created","data":{}}',
              responseBody: '{"error":"service unavailable"}',
              durationMs: 1234,
              responseHeaders: { "Retry-After": "120" },
            },
          }),
        });
      },
    );

    // Use the new flat route with ?from= for context.
    await page.goto(
      `/dash0/orgs/test/notifications/${notifUid}?from=incident:11111111-1111-1111-1111-111111111111`,
    );
    await page.waitForLoadState("networkidle");

    // The Delivery section is present with the status-code badge and duration.
    await expect(page.getByText("Delivery", { exact: true })).toBeVisible();
    await expect(page.getByText("503", { exact: true })).toBeVisible();
    await expect(page.getByText("1234 ms")).toBeVisible();
    await expect(
      page.getByText("https://hooks.example.com/incidents"),
    ).toBeVisible();

    // The response body is copyable: expand and copy it.
    const responseSummary = page.getByText("Response body");
    await expect(responseSummary).toBeVisible();
    await page
      .getByRole("button", { name: /Copy Response body/i })
      .click();
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    expect(clip).toContain("service unavailable");

    // The request payload is also offered as a collapsible.
    await expect(page.getByText("Request payload")).toBeVisible();
  });

  // Criterion 5: a notification with no deliveryDetails renders the page with
  // NO Delivery section — no crash, no empty box.
  test("notification without delivery details shows no Delivery section", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const notifUid = "55555555-5555-5555-5555-555555555555";

    await page.route(
      `**/api/v1/orgs/test/notifications/${notifUid}`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: notifUid,
            incidentUid: "44444444-4444-4444-4444-444444444444",
            eventType: "incident.created",
            source: "escalation_user",
            channelType: "email",
            status: "sent",
            createdAt: "2026-06-02T10:00:00Z",
            sentAt: "2026-06-02T10:00:02Z",
            user: { uid: "u-1", name: "On-call" },
            connection: null,
          }),
        });
      },
    );

    // Use flat route with ?from=integration: to test the integration breadcrumb.
    await page.goto(
      `/dash0/orgs/test/notifications/${notifUid}?from=integration:some-integration-uid`,
    );
    await page.waitForLoadState("networkidle");

    // The page renders normally...
    await expect(page.getByRole("heading", { name: "Notification" })).toBeVisible();
    await expect(page.getByText("Delivery timeline")).toBeVisible();
    // ...but the Delivery section (exact "Delivery" card title) is absent.
    await expect(page.getByText("Delivery", { exact: true })).toHaveCount(0);
  });
});
