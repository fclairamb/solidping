import { test, expect, type Page } from "./fixtures";

const API_BASE = (
  process.env.E2E_BASE_URL ?? "http://localhost:4000/dash0/"
).replace(/\/dash0\/?$/, "");

// --- Helpers for the "Checks at a glance" tests -------------------------------
// These tests mock the dashboard's data endpoints so the glance card is
// deterministic regardless of seeded data or background polling. The dashboard
// issues two /results queries (periodType=day for the KPI, periodType=hour for
// the uptime strips); we serve both from one route handler.

interface MockCheck {
  uid: string;
  name: string;
  status: "up" | "down" | "warning";
  enabled?: boolean;
  lastStatusChangeTime?: string;
}

function hourlyResultsFor(checkUid: string, availabilityPct: number) {
  // 24 hourly buckets ending now, all with the same availability + latency.
  return Array.from({ length: 24 }, (_, i) => ({
    uid: `${checkUid}-h${i}`,
    checkUid,
    periodType: "hour",
    periodStart: new Date(Date.now() - (23 - i) * 3600_000).toISOString(),
    status: availabilityPct >= 100 ? "up" : "down",
    availabilityPct,
    durationMs: 142,
    totalChecks: 60,
    successfulChecks: Math.round((availabilityPct / 100) * 60),
  }));
}

async function mockDashboard(
  page: Page,
  opts: { checks: MockCheck[]; incidents?: unknown[]; events?: unknown[] },
) {
  const checks = opts.checks.map((c) => ({
    uid: c.uid,
    name: c.name,
    type: "http",
    enabled: c.enabled ?? true,
    status: c.status,
    lastResult: { status: c.status, durationMs: 142 },
    lastStatusChange: c.lastStatusChangeTime
      ? { status: c.status, time: c.lastStatusChangeTime }
      : undefined,
  }));

  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (!url.includes("/checks")) return route.continue();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: checks, pagination: { total: checks.length } }),
    });
  });

  await page.route("**/api/v1/orgs/*/incidents*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.incidents ?? [],
        pagination: { total: (opts.incidents ?? []).length },
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/results*", (route) => {
    const url = route.request().url();
    if (!url.includes("/results")) return route.continue();
    const isHour = url.includes("periodType=hour");
    const data = isHour
      ? opts.checks.flatMap((c) =>
          hourlyResultsFor(c.uid, c.status === "up" ? 100 : 0),
        )
      : opts.checks.map((c) => ({
          uid: `${c.uid}-day`,
          checkUid: c.uid,
          periodType: "day",
          periodStart: new Date().toISOString(),
          status: c.status,
          availabilityPct: c.status === "up" ? 100 : 0,
          totalChecks: 60,
          successfulChecks: c.status === "up" ? 60 : 0,
        }));
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data, pagination: { total: data.length } }),
    });
  });

  await page.route("**/api/v1/orgs/*/events*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.events ?? [],
        pagination: { total: (opts.events ?? []).length },
      }),
    }),
  );
}

test.describe("Dashboard", () => {
  test("should land on org dashboard after login", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Wait for page to load
    await page.waitForLoadState("networkidle");

    // Take screenshot of dashboard
    await page.screenshot({
      path: "test-results/screenshots/dashboard.png",
      fullPage: true,
    });

    // Spec #46 lands login on /orgs/$org — the operator welcome page — not /checks.
    expect(page.url()).not.toContain("/login");
    expect(page.url()).toMatch(/\/orgs\/test\/?$/);

    // Sidebar is the cheapest way to confirm we rendered an authenticated page.
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
  });

  test("should display sidebar navigation", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // The sidebar should be visible with navigation items
    const sidebar = page.locator('[data-testid="app-sidebar"]');
    const sidebarTrigger = page.locator('[data-testid="sidebar-trigger"]');

    // Either sidebar or sidebar trigger should be present
    const hasSidebar =
      (await sidebar.isVisible()) || (await sidebarTrigger.isVisible());
    expect(hasSidebar).toBe(true);

    // Take screenshot of sidebar navigation
    await page.screenshot({
      path: "test-results/screenshots/dashboard-sidebar.png",
      fullPage: true,
    });
  });

  test("should show loading state then content", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Navigate to org root - should redirect to checks
    await page.goto("orgs/test");
    await page.waitForLoadState("domcontentloaded");

    // Wait for content to load
    await page.waitForSelector("body", { state: "visible" });

    // Eventually, the page should show real content
    await page.waitForLoadState("networkidle");

    // Take final screenshot
    await page.screenshot({
      path: "test-results/screenshots/dashboard-loaded.png",
      fullPage: true,
    });
  });

  test("should not have content cut off by sidebar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Check that the "Checks" heading starts after the sidebar
    const checksHeading = page.getByRole("heading", { name: "Checks", exact: true });
    if (await checksHeading.isVisible()) {
      const boundingBox = await checksHeading.boundingBox();
      expect(boundingBox).not.toBeNull();
      // The text should start after the sidebar (at least 250px from left edge)
      expect(boundingBox!.x).toBeGreaterThan(250);
    }

    // Take screenshot for visual verification
    await page.screenshot({
      path: "test-results/screenshots/dashboard-sidebar-layout.png",
      fullPage: true,
    });
  });

  test("Recent activity footer shows a single arrow", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    const footer = page.getByTestId("recent-activity-footer");
    // The recent-activity card only renders for a non-empty org. When the
    // seeded test org has checks this is visible; otherwise skip the assertion.
    if (await footer.isVisible().catch(() => false)) {
      const text = (await footer.innerText()).trim();
      // Exactly one right-arrow — the duplicate "→ →" bug is gone.
      const arrowCount = (text.match(/→/g) || []).length;
      expect(arrowCount).toBe(1);
      expect(text).toMatch(/→$/);
    }
  });

  test("recent activity renders resolved labels and activation descriptions", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Authenticate against the API to emit the activation milestone directly.
    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const token = (await loginResp.json()).accessToken as string;

    // Creating the first check emits org.activation.first_check_created, which
    // surfaces in the dashboard's recent-activity feed with its description.
    await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: "Activation feed probe",
        type: "http",
        config: { url: "https://example.com" },
      },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const feed = page.getByTestId("recent-activity-footer");
    // Recent-activity card renders for a non-empty org (we just created a check).
    await expect(feed).toBeVisible({ timeout: 10000 });

    // Labels must be resolved — no raw event keys leak into the feed.
    await expect(page.getByText("org.activation.first_check_created")).toHaveCount(0);

    // When the activation milestone is in the feed, its description renders.
    const desc = page.getByText("Your first uptime check is live and monitoring.");
    const label = page.getByText("First Check Created", { exact: true });
    if (await label.first().isVisible().catch(() => false)) {
      await expect(desc.first()).toBeVisible();
    }

    // The check.created event surfaces the check's name as a link to its detail page.
    const nameLink = page.getByRole("link", { name: "Activation feed probe" });
    await expect(nameLink.first()).toBeVisible();
    await expect(nameLink.first()).toHaveAttribute("href", /\/checks\//);
  });

  test("KPI tiles navigate to the right list pages", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    // Monitored tile → checks list
    await page.getByTestId("kpi-tile-monitored").click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/checks\/?$/);

    await page.goBack();
    await page.waitForLoadState("networkidle");

    // Down tile → checks list filtered to failing statuses
    await page.getByTestId("kpi-tile-down").click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toContain("/orgs/test/checks");
    expect(page.url()).toContain("status=down");

    await page.goBack();
    await page.waitForLoadState("networkidle");

    // Active incidents tile → incidents list with state=active
    await page.getByTestId("kpi-tile-incidents").click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toContain("/orgs/test/incidents");
    expect(page.url()).toContain("state=active");
  });

  test("healthy org: glance card lists checks with strips, no incidents card, footer links to /checks", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockDashboard(page, {
      checks: [
        { uid: "11111111-1111-1111-1111-111111111111", name: "Alpha API", status: "up" },
        { uid: "22222222-2222-2222-2222-222222222222", name: "Beta Web", status: "up" },
        { uid: "33333333-3333-3333-3333-333333333333", name: "Gamma Job", status: "up" },
      ],
      incidents: [],
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    // Glance card is present and lists the seeded checks.
    const glance = page.getByTestId("checks-glance");
    await expect(glance).toBeVisible({ timeout: 10000 });
    await expect(glance.getByText("Checks at a glance")).toBeVisible();

    const rows = page.getByTestId("glance-row");
    await expect(rows).toHaveCount(3);
    await expect(glance.getByText("Alpha API")).toBeVisible();
    await expect(glance.getByText("Beta Web")).toBeVisible();
    await expect(glance.getByText("Gamma Job")).toBeVisible();

    // No active-incidents card in the healthy state (the KPI tile of the same
    // name still exists; the card is gated on incidentsCount > 0).
    await expect(page.getByTestId("active-incidents")).toHaveCount(0);

    // Footer references the total count and links to the checks list.
    const footer = page.getByTestId("checks-glance-footer");
    await expect(footer).toBeVisible();
    await expect(footer).toContainText("3");
    await footer.click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/checks\/?$/);
  });

  test("a down check sorts first with a destructive badge and a since timestamp", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockDashboard(page, {
      checks: [
        { uid: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", name: "Healthy One", status: "up" },
        {
          uid: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
          name: "Broken Service",
          status: "down",
          lastStatusChangeTime: new Date(Date.now() - 12 * 60 * 1000).toISOString(),
        },
      ],
      incidents: [],
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const glance = page.getByTestId("checks-glance");
    await expect(glance).toBeVisible({ timeout: 10000 });

    const rows = page.getByTestId("glance-row");
    await expect(rows).toHaveCount(2);

    // Down check sorts first.
    await expect(rows.first()).toContainText("Broken Service");

    // It carries a destructive status badge (down) and a "since" timestamp.
    // StatusBadge renders the localized label ("Down"), not the raw token.
    await expect(rows.first().getByText("Down", { exact: true })).toBeVisible();
    await expect(rows.first()).toContainText("since");
  });

  test("glance list badges render the shared StatusBadge colors, not plain gray chips", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockDashboard(page, {
      checks: [
        { uid: "d1111111-1111-1111-1111-111111111111", name: "Up Service", status: "up" },
        { uid: "d2222222-2222-2222-2222-222222222222", name: "Warning Service", status: "warning" },
        { uid: "d3333333-3333-3333-3333-333333333333", name: "Down Service", status: "down" },
      ],
      incidents: [],
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const rows = page.getByTestId("glance-row");
    await expect(rows).toHaveCount(3);

    // "up" renders the success (green) badge variant with the localized label.
    const upRow = rows.filter({ hasText: "Up Service" });
    await expect(upRow.getByText("Up", { exact: true })).toHaveClass(/bg-green-500/);

    // "warning" renders the warning (amber) badge variant — previously a plain
    // gray "outline" chip indistinguishable from a healthy check.
    const warningRow = rows.filter({ hasText: "Warning Service" });
    await expect(warningRow.getByText("Warning", { exact: true })).toHaveClass(
      /bg-yellow-500/,
    );

    // "down" renders the destructive (red) badge variant.
    const downRow = rows.filter({ hasText: "Down Service" });
    await expect(downRow.getByText("Down", { exact: true })).toHaveClass(
      /bg-destructive/,
    );
  });

  test("with an active incident, the incidents card renders above the glance card", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockDashboard(page, {
      checks: [
        { uid: "cccccccc-cccc-cccc-cccc-cccccccccccc", name: "Watched Service", status: "up" },
      ],
      incidents: [
        {
          uid: "dddddddd-dddd-dddd-dddd-dddddddddddd",
          title: "Database latency spike",
          state: "active",
          startedAt: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
        },
      ],
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const glance = page.getByTestId("checks-glance");
    await expect(glance).toBeVisible({ timeout: 10000 });

    // Incidents card is present and shows the incident.
    const incidentsCard = page.getByTestId("active-incidents");
    await expect(incidentsCard).toBeVisible();
    await expect(incidentsCard.getByText("Database latency spike")).toBeVisible();

    // Incidents card sits above the glance card in document order (higher = lower y).
    const incidentBox = await incidentsCard.boundingBox();
    const glanceBox = await glance.boundingBox();
    expect(incidentBox).not.toBeNull();
    expect(glanceBox).not.toBeNull();
    expect(incidentBox!.y).toBeLessThan(glanceBox!.y);
  });

  test("Recent activity row for an incident event shows both incident and check links", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checkUid = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee";
    const incidentUid = "ffffffff-ffff-ffff-ffff-ffffffffffff";

    await mockDashboard(page, {
      checks: [{ uid: checkUid, name: "Payments API", status: "down" }],
      incidents: [],
      events: [
        {
          uid: "event-incident-created",
          eventType: "incident.created",
          actorType: "system",
          checkUid,
          incidentUid,
          payload: {
            check_uid: checkUid,
            check_slug: "payments-api",
            check_name: "Payments API",
          },
          createdAt: new Date().toISOString(),
        },
      ],
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const feed = page.getByTestId("recent-activity-footer");
    await expect(feed).toBeVisible({ timeout: 10000 });

    // Both links must be present: the incident link and a named check link.
    // exact: true disambiguates from the sidebar's "Incidents" (plural) nav
    // link, which also matches a substring "name" query.
    const incidentLink = page.getByRole("link", { name: "Incident", exact: true });
    await expect(incidentLink.first()).toBeVisible();
    await expect(incidentLink.first()).toHaveAttribute(
      "href",
      new RegExp(`/incidents/${incidentUid}`),
    );

    const checkLink = page.getByRole("link", { name: "Payments API", exact: true });
    await expect(checkLink.first()).toBeVisible();
    await expect(checkLink.first()).toHaveAttribute(
      "href",
      new RegExp(`/checks/${checkUid}`),
    );
  });

  test("Recent activity row for a historical incident event (no check_name) falls back to check_slug", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checkUid = "12121212-1212-1212-1212-121212121212";
    const incidentUid = "13131313-1313-1313-1313-131313131313";

    await mockDashboard(page, {
      checks: [{ uid: checkUid, name: "Legacy Check", status: "down" }],
      incidents: [],
      events: [
        {
          uid: "event-incident-resolved-old",
          eventType: "incident.resolved",
          actorType: "system",
          checkUid,
          incidentUid,
          // Historical shape: no check_name captured, only check_slug.
          payload: {
            check_uid: checkUid,
            check_slug: "legacy-check",
          },
          createdAt: new Date().toISOString(),
        },
      ],
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const feed = page.getByTestId("recent-activity-footer");
    await expect(feed).toBeVisible({ timeout: 10000 });

    // Falls back to the slug as the link text since check_name is absent.
    const checkLink = page.getByRole("link", { name: "legacy-check" });
    await expect(checkLink.first()).toBeVisible();
    await expect(checkLink.first()).toHaveAttribute(
      "href",
      new RegExp(`/checks/${checkUid}`),
    );
  });

  test("Recent activity shows the notification channel name and links to the integration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Real API round-trip: create a channel, which emits
    // org.activation.first_notification_configured enriched with
    // channel_uid/channel_name/channel_type.
    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const token = (await loginResp.json()).accessToken as string;

    const channelName = `E2E Recent Activity Channel ${Date.now()}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/channels`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "webhook",
          name: channelName,
          settings: { url: "https://example.com/hook" },
        },
      },
    );
    const channel = await createResp.json();

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const feed = page.getByTestId("recent-activity-footer");
    await expect(feed).toBeVisible({ timeout: 10000 });

    // The channel name renders as a link to its integration detail page —
    // but only for the FIRST channel ever created in this org; on a reused
    // test org where the milestone already fired for an earlier channel,
    // skip rather than flake.
    const channelLink = page.getByRole("link", { name: channelName });
    if (await channelLink.first().isVisible().catch(() => false)) {
      await expect(channelLink.first()).toHaveAttribute(
        "href",
        new RegExp(`/integrations/${channel.uid}`),
      );
    }

    // Clean up.
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/channels/${channel.uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  });
});
