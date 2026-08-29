import { test, expect, type Page, API_BASE } from "./fixtures";

// --- Helpers for the "Checks at a glance" tests -------------------------------
// These tests mock the dashboard's data endpoints so the glance card is
// deterministic regardless of seeded data or background polling. The 24h
// availability KPI comes from GET /checks/stats (availability24h), not from a
// /results query (spec 2026-08-26-09) — the one /results query left is
// periodType=hour, which feeds the glance-card uptime strips only.

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

interface MockCheckStats {
  total: number;
  enabled: number;
  disabled: number;
  byStatus: Record<string, number>;
  down: number;
  hardDown: number;
  availability24h: number | null;
}

// Aggregate the mocked checks the same way the backend's GROUP BY would, so a
// test that doesn't care about the stats endpoint keeps the counters it always
// had. Tests that DO care pass an explicit `stats` override.
//
// availability24h defaults to null (never a fabricated number) — it comes
// from a server-side aggregate the mocked `checks` list has no bearing on;
// tests that care about the KPI/badge pass an explicit override.
function statsFromChecks(checks: MockCheck[]): MockCheckStats {
  const byStatus: Record<string, number> = {
    created: 0,
    up: 0,
    down: 0,
    validating: 0,
    degraded: 0,
    warning: 0,
    unknown: 0,
  };
  for (const c of checks) byStatus[c.status] = (byStatus[c.status] ?? 0) + 1;
  const enabled = checks.filter((c) => c.enabled !== false).length;
  return {
    total: checks.length,
    enabled,
    disabled: checks.length - enabled,
    byStatus,
    down: byStatus.down,
    hardDown: byStatus.down,
    availability24h: null,
  };
}

async function mockDashboard(
  page: Page,
  opts: {
    checks: MockCheck[];
    incidents?: unknown[];
    events?: unknown[];
    /**
     * Overrides for GET /checks/stats. Deliberately independent of `checks`:
     * the dashboard's counters must come from this endpoint, never from the
     * (page-clamped) checks list.
     */
    stats?: Partial<MockCheckStats>;
    /**
     * Overrides `pagination.total` on GET /incidents independently of
     * `incidents.length` — the dashboard requests only `size: 5`, so a real
     * org with more active incidents than that returns a truncated `data`
     * array alongside the untruncated total. Defaults to `incidents.length`
     * when omitted, matching a page that fits within one request.
     */
    incidentsTotal?: number;
  },
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

  const stats: MockCheckStats = { ...statsFromChecks(opts.checks), ...opts.stats };

  // Needs its own pattern: in Playwright URL globs a single `*` does not match
  // `/`, so `**/checks*` matches `/checks?limit=…` but never `/checks/stats`.
  // Without this route the dashboard's counters would come from the real
  // server and the mocked list below would silently disagree with them.
  await page.route("**/api/v1/orgs/*/checks/stats*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(stats),
    }),
  );

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
        pagination: { total: opts.incidentsTotal ?? (opts.incidents ?? []).length },
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/results*", (route) => {
    const url = route.request().url();
    if (!url.includes("/results")) return route.continue();
    const data = opts.checks.flatMap((c) =>
      hourlyResultsFor(c.uid, c.status === "up" ? 100 : 0),
    );
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
    const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: "Activation feed probe",
        type: "http",
        config: { url: "https://example.com" },
      },
    });
    // Asserted, not fire-and-forget: an unchecked creation turns a server-side
    // failure into "the activity link never appeared", 5000 ms and one
    // screenshot later, with nothing pointing at the POST. (It was exactly
    // that: the auto-slug ladder ran out of "-N" suffixes once the suite had
    // accumulated 99 example.com checks in the shared test org, and this
    // creation 500'd.)
    expect(
      created.status(),
      `POST /checks -> ${await created.text()}`,
    ).toBeLessThan(300);

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

  test("KPI counters come from /checks/stats, not from the clamped checks page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // The scenario from GitHub issue #172: an org far larger than one page.
    // The checks list serves 3 rows (a real one would serve at most 100);
    // the stats endpoint reports the true fleet. Every counter on the page
    // must read the latter — if any is still derived from the list, it shows
    // 3 / 2 / 1 instead.
    await mockDashboard(page, {
      checks: [
        { uid: "51111111-1111-1111-1111-111111111111", name: "Sample A", status: "up" },
        { uid: "52222222-2222-2222-2222-222222222222", name: "Sample B", status: "up" },
        { uid: "53333333-3333-3333-3333-333333333333", name: "Sample C", status: "down" },
      ],
      incidents: [],
      stats: {
        total: 262,
        enabled: 250,
        disabled: 12,
        byStatus: {
          created: 0,
          up: 214,
          down: 42,
          validating: 0,
          degraded: 6,
          warning: 0,
          unknown: 0,
        },
        down: 42,
        hardDown: 42,
      },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    // "Monitored" tile shows the org-wide enabled count and the disabled sub.
    const monitored = page.getByTestId("kpi-tile-monitored");
    await expect(monitored).toBeVisible({ timeout: 10000 });
    await expect(monitored).toContainText("250");
    await expect(monitored).toContainText("12");

    // "Down" tile shows the org-wide down count, not the 1 down row on screen.
    const down = page.getByTestId("kpi-tile-down");
    await expect(down).toContainText("42");

    // The glance footer's total is the fleet size, not the page size.
    const footer = page.getByTestId("checks-glance-footer");
    await expect(footer).toContainText("262");

    // The card itself still renders only the rows the list returned.
    await expect(page.getByTestId("glance-row")).toHaveCount(3);
  });

  // --- 24h availability KPI (spec 2026-08-26-09) -----------------------------
  // The tile used to be fabricated theater: a structurally-dead /results
  // query always returned zero rows, and the null case rendered the literal
  // fallback "100%"/"Operational" — for every org, all the time. These tests
  // pin the fix: the tile and banner read stats.availability24h and are
  // honest about having no data.

  test("24h availability tile shows an em dash and 'No data' when availability24h is null, never a fabricated 100%", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockDashboard(page, {
      checks: [
        { uid: "61111111-1111-1111-1111-111111111111", name: "Fresh Check", status: "up" },
      ],
      incidents: [],
      stats: { availability24h: null },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const tile = page.getByTestId("kpi-tile-availability");
    await expect(tile).toBeVisible({ timeout: 10000 });
    await expect(tile).toContainText("—");
    await expect(tile).toContainText("No data");
    await expect(tile).not.toContainText("100%");
    await expect(tile).not.toContainText("Operational");

    // The all-green banner must not claim an SLA figure it doesn't have —
    // the "24h SLA Operational" pill is suppressed entirely when there is no
    // data (down=0, incidents=0 puts this scenario on the all-green branch).
    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible();
    await expect(banner).not.toContainText("100%");
    await expect(banner).not.toContainText("SLA Operational");
  });

  test("24h availability badge tiers follow the real percentage: Operational / Degraded / Down", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const cases: { pct: number; label: string }[] = [
      { pct: 99.95, label: "Operational" },
      { pct: 99.5, label: "Degraded" },
      { pct: 92, label: "Down" },
    ];

    for (const { pct, label } of cases) {
      await mockDashboard(page, {
        checks: [
          { uid: "62222222-2222-2222-2222-222222222222", name: "Tiered Check", status: "up" },
        ],
        incidents: [],
        stats: { availability24h: pct },
      });

      await page.goto("orgs/test");
      await page.waitForLoadState("networkidle");

      const tile = page.getByTestId("kpi-tile-availability");
      await expect(tile).toBeVisible({ timeout: 10000 });
      await expect(tile).toContainText(pct.toFixed(2));
      await expect(tile).toContainText(label);
    }
  });

  test("the all-green banner shows the real availability figure and SLA pill when data exists", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockDashboard(page, {
      checks: [
        { uid: "63333333-3333-3333-3333-333333333333", name: "Healthy Check", status: "up" },
      ],
      incidents: [],
      stats: { availability24h: 99.97 },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });
    await expect(banner).toContainText("99.97");
    await expect(banner).toContainText("SLA Operational");

    const tile = page.getByTestId("kpi-tile-availability");
    await expect(tile).toContainText("99.97%");
    await expect(tile).toContainText("Operational");
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
    await expect(upRow.getByText("Up", { exact: true })).toHaveClass(
      /bg-status-ok\/15/,
    );

    // "warning" renders the warning (amber) badge variant — previously a plain
    // gray "outline" chip indistinguishable from a healthy check.
    const warningRow = rows.filter({ hasText: "Warning Service" });
    await expect(warningRow.getByText("Warning", { exact: true })).toHaveClass(
      /bg-status-warning\/15/,
    );

    // "down" renders the destructive (red) badge variant.
    const downRow = rows.filter({ hasText: "Down Service" });
    await expect(downRow.getByText("Down", { exact: true })).toHaveClass(
      /bg-status-error\/15/,
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

  test("active-incidents KPI shows the server-side total, not the truncated page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // The dashboard requests the incidents list with `size: 5` — mock 5 rows
    // (the page) but an org-wide active total of 9, mirroring the "GitHub
    // issue #172" checks-stats scenario above. Before this fix, the KPI tile
    // and banner both derived the count from `data.length` and would show 5.
    const incidents = Array.from({ length: 5 }, (_, i) => ({
      uid: `99999999-9999-9999-9999-99999999999${i}`,
      title: `Incident ${i}`,
      state: "active",
      startedAt: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    }));

    await mockDashboard(page, {
      checks: [
        { uid: "61111111-1111-1111-1111-111111111111", name: "Sample A", status: "down" },
      ],
      incidents,
      incidentsTotal: 9,
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const incidentsTile = page.getByTestId("kpi-tile-incidents");
    await expect(incidentsTile).toBeVisible({ timeout: 10000 });
    await expect(incidentsTile).toContainText("9");
    await expect(incidentsTile).not.toContainText("5");

    // The card below the tile still renders only the 5 rows the list
    // returned — only the tile number and banner copy read the total.
    const incidentsCard = page.getByTestId("active-incidents");
    await expect(incidentsCard).toBeVisible();
    await expect(incidentsCard.getByText("Incident 0")).toBeVisible();
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

  // --- Clickable status banner (spec 2026-08-28-10) --------------------------
  // The banner used to be a dead end: an operator who saw "Issues detected"
  // had to hunt through the sidebar for the incidents page or the checks
  // list. It is now a real <Link>, with incidents taking priority over a
  // bare down check (an active incident carries the ack/snooze/resolve
  // workflow; a down check without one is just a state).

  test("with an active incident, the red banner links to the incidents list filtered to active", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockDashboard(page, {
      checks: [
        { uid: "a1111111-1111-1111-1111-111111111111", name: "Payments API", status: "down" },
      ],
      incidents: [
        {
          uid: "a2222222-2222-2222-2222-222222222222",
          title: "Payments API is down",
          state: "active",
          startedAt: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
        },
      ],
      stats: { down: 1, hardDown: 1 },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });
    await expect(banner).toContainText("Issues detected");

    await banner.click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/incidents\?state=active/);
  });

  test("with only a down check (no active incident), the red banner links to the checks list filtered to down", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockDashboard(page, {
      checks: [
        { uid: "a3333333-3333-3333-3333-333333333333", name: "Payments API", status: "down" },
      ],
      incidents: [],
      stats: { down: 1, hardDown: 1 },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });

    await banner.click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/checks\?status=down/);
  });

  test("the amber degraded banner links to the checks list filtered to warning", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Timeouts only: down > 0 but hardDown = 0 keeps the banner on the amber
    // "Degraded Performance" branch (dashboard-page.tsx's
    // timeoutOnlyCount = downCount - hardDownCount).
    await mockDashboard(page, {
      checks: [
        { uid: "a4444444-4444-4444-4444-444444444444", name: "Slow API", status: "warning" },
      ],
      incidents: [],
      stats: { down: 1, hardDown: 0 },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });
    await expect(banner).toContainText("Some checks degraded");

    await banner.click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/checks\?status=warning/);
  });

  test("recovered check but open incident: subtitle names the incident and never claims 'No active incidents'", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // The exact regression from the spec: hardDownCount = 0 (the check
    // recovered) but incidentsCount > 0 (the incident is still open) --
    // exactly the state that keeps the RED branch alive via
    // `hardDownCount > 0 || incidentsCount > 0`. Before the fix, a single
    // `count: hardDownCount` selector picked `issuesSub_zero`, "No active
    // incidents", flatly contradicting the banner firing because of that
    // incident.
    await mockDashboard(page, {
      checks: [
        { uid: "a5555555-5555-5555-5555-555555555555", name: "Payments API", status: "up" },
      ],
      incidents: [
        {
          uid: "a6666666-6666-6666-6666-666666666666",
          title: "Payments API had an outage",
          state: "active",
          startedAt: new Date(Date.now() - 30 * 60 * 1000).toISOString(),
        },
      ],
      stats: { down: 0, hardDown: 0 },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });
    await expect(banner).toContainText("Issues detected");
    await expect(banner).toContainText("1 active incident");
    await expect(banner).not.toContainText("No active incident");
    await expect(banner).not.toContainText("check down");

    // Still links to the incidents list, since incidentsCount > 0.
    await banner.click();
    await page.waitForLoadState("networkidle");
    expect(page.url()).toMatch(/\/orgs\/test\/incidents\?state=active/);
  });

  test("with both a down check and an active incident, each subtitle fragment pluralizes on its own count", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const incidents = Array.from({ length: 1 }, (_, i) => ({
      uid: `a777777${i}-7777-7777-7777-777777777777`,
      title: `Incident ${i}`,
      state: "active",
      startedAt: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    }));

    await mockDashboard(page, {
      checks: [
        { uid: "a8888888-8888-8888-8888-888888888881", name: "API One", status: "down" },
        { uid: "a8888888-8888-8888-8888-888888888882", name: "API Two", status: "down" },
      ],
      incidents,
      stats: { down: 2, hardDown: 2 },
    });

    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const banner = page.getByTestId("overall-status-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });
    // "2 checks down" (plural) AND "1 active incident" (singular) -- each
    // fragment reflects its OWN count, not a single shared selector.
    await expect(banner).toContainText("2 checks down");
    await expect(banner).toContainText("1 active incident");
    await expect(banner).not.toContainText("1 active incidents");
  });
});
