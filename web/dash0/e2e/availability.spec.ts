import { test, expect, type Page, mockSloCoverage } from "./fixtures";

// The availability card reads the server-side per-period endpoint
// (GET /checks/:uid/availability). These tests mock that endpoint so the table
// renders deterministic data regardless of the freshly-created check's real
// history.

type Period = {
  period: string;
  windowStart: string;
  windowEnd: string;
  monitoredSeconds: number;
  partial: boolean;
  hasData: boolean;
  totalChecks: number;
  successfulChecks: number;
  availabilityPct: number | null;
  downtimeSeconds: number;
  incidents: {
    count: number;
    longestSeconds?: number;
    averageSeconds?: number;
    totalDowntimeSeconds?: number;
  };
};

function makePeriod(token: string, overrides: Partial<Period> = {}): Period {
  return {
    period: token,
    windowStart: "2026-06-01T00:00:00Z",
    windowEnd: "2026-06-30T00:00:00Z",
    monitoredSeconds: 46_828, // ~0.54 day → "1d monitored"
    partial: true,
    hasData: true,
    totalChecks: 100,
    successfulChecks: 99,
    availabilityPct: 99.43,
    downtimeSeconds: 267, // → "4m 27s"
    incidents: {
      count: 1,
      longestSeconds: 624, // → "10m 24s"
      averageSeconds: 624,
      totalDowntimeSeconds: 624,
    },
    ...overrides,
  };
}

async function mockAvailability(page: Page, periods: Period[]) {
  await page.route("**/api/v1/orgs/*/checks/*/availability*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: periods }),
    }),
  );
  await page.route("**/api/v1/orgs/*/incidents*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
    }),
  );
  // The check-detail header's SLO coverage chip fires an unconditional
  // GET /slos?checkUid=… on every load (spec 2026-08-20-01); left unmocked it
  // is one more real request this spec's networkidle wait has to sit through.
  await mockSloCoverage(page);
}

async function createCheckAndOpen(page: Page, name: string) {
  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");

  await page.getByTestId("check-name-input").fill(name);
  await page
    .getByTestId("check-url-input")
    .fill("https://example.com/avail-test");
  await page.getByTestId("check-submit-button").click();

  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

function availabilityTable(page: Page) {
  return page.locator("table").filter({ hasText: "Time period" });
}

test.describe("Availability Table", () => {
  test("collapses provably-identical partial periods into one 'Since creation' row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // A check younger than every window: the server returns four partial
    // periods that all measure the same span, so they are byte-identical. The
    // table must not repeat them — it shows a single catch-all row.
    await mockAvailability(page, [
      makePeriod("1h"),
      makePeriod("24h"),
      makePeriod("30d"),
      makePeriod("90d"),
      makePeriod("365d"),
    ]);

    await createCheckAndOpen(page, `E2E Avail Collapse ${Date.now()}`);

    const table = availabilityTable(page);
    await expect(table).toBeVisible({ timeout: 10000 });

    // Exactly one data row, labelled "Since creation" with the real days of data.
    await expect(table.locator("tbody tr")).toHaveCount(1);
    await expect(table.getByText("Since creation")).toBeVisible();
    await expect(table.getByText("1d monitored")).toBeVisible();

    // The fixed-window labels are gone — no more copy-paste rows.
    await expect(table.getByText("Last hour")).toHaveCount(0);
    await expect(table.getByText("Last 24 hours")).toHaveCount(0);
    await expect(table.getByText("Last 30 days")).toHaveCount(0);
    await expect(table.getByText("Last 90 days")).toHaveCount(0);
    await expect(table.getByText("Last 365 days")).toHaveCount(0);

    // The values appear exactly once instead of four times.
    await expect(table.getByText("99.43%")).toHaveCount(1);
    await expect(table.getByText("4m 27s")).toHaveCount(1);
    await expect(table.getByText("10m 24s")).toHaveCount(2); // longest + avg
  });

  test("shows every distinct window for a mature check (none partial)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // A check older than 365 days: each window is genuinely distinct, so all
    // four rows render and nothing collapses.
    await mockAvailability(page, [
      makePeriod("1h", { partial: false, availabilityPct: 99.99, incidents: { count: 0 } }),
      makePeriod("24h", { partial: false, availabilityPct: 99.5, incidents: { count: 0 } }),
      makePeriod("30d", { partial: false, availabilityPct: 98, incidents: { count: 0 } }),
      makePeriod("90d", { partial: false, availabilityPct: 97, incidents: { count: 0 } }),
      makePeriod("365d", { partial: false, availabilityPct: 95, incidents: { count: 0 } }),
    ]);

    await createCheckAndOpen(page, `E2E Avail Mature ${Date.now()}`);

    const table = availabilityTable(page);
    await expect(table).toBeVisible({ timeout: 10000 });

    await expect(table.locator("tbody tr")).toHaveCount(5);
    await expect(table.getByText("Last hour")).toBeVisible();
    await expect(table.getByText("Last 24 hours")).toBeVisible();
    await expect(table.getByText("Last 30 days")).toBeVisible();
    await expect(table.getByText("Last 90 days")).toBeVisible();
    await expect(table.getByText("Last 365 days")).toBeVisible();

    // No collapsed row and no "Nd monitored" caption for a mature check.
    await expect(table.getByText("Since creation")).toHaveCount(0);
    await expect(table.getByText(/monitored/)).toHaveCount(0);

    await expect(table.getByText("99.99%")).toBeVisible();
    await expect(table.getByText("95.0%")).toBeVisible();
  });

  test("rows are inert — clicking does not navigate or change the chart", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockAvailability(page, [
      makePeriod("1h", { partial: false, availabilityPct: 100, downtimeSeconds: 0, incidents: { count: 0 } }),
      makePeriod("24h", { partial: false, availabilityPct: 100, downtimeSeconds: 0, incidents: { count: 0 } }),
      makePeriod("30d", { partial: false, availabilityPct: 100, downtimeSeconds: 0, incidents: { count: 0 } }),
      makePeriod("90d", { partial: false, availabilityPct: 100, downtimeSeconds: 0, incidents: { count: 0 } }),
      makePeriod("365d", { partial: false, availabilityPct: 100, downtimeSeconds: 0, incidents: { count: 0 } }),
    ]);

    await createCheckAndOpen(page, `E2E Avail Inert ${Date.now()}`);

    const table = availabilityTable(page);
    await expect(table).toBeVisible({ timeout: 10000 });

    // No row advertises itself as clickable.
    await expect(table.locator("tbody tr.cursor-pointer")).toHaveCount(0);

    // Clicking a row must not push a chart period into the URL (the old
    // drill-into-chart behaviour) or otherwise navigate.
    const before = page.url();
    await table.locator("tbody tr").first().click();
    await page.waitForTimeout(300);
    expect(page.url()).toBe(before);
    expect(page.url()).not.toContain("graphPeriod");
  });
});
