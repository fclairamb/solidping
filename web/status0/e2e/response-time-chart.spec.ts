import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * Public status page response-time chart — per-region series (spec
 * 2026-08-14-04).
 *
 * Mocks the public status page API directly (following
 * overall-status-badge.spec.ts's pattern) so both the single-series and the
 * multi-region paths are deterministic, instead of depending on whatever
 * regions the local dev seed happens to have.
 */
const ORG = "e2e-response-time";
const SLUG = "response-time";

function isoMinutesAgo(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function basePayload() {
  return {
    uid: "22222222-2222-2222-2222-222222222222",
    name: "Chart Demo Page",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: true,
    historyDays: 7,
    historyPeriod: "7d",
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
    overallStatus: "operational",
  };
}

function resourceWithSeries(responseTimeSeries: unknown[]) {
  return {
    uid: "33333333-3333-3333-3333-333333333333",
    checkUid: "44444444-4444-4444-4444-444444444444",
    position: 0,
    check: { name: "API", type: "http", status: "up", inMaintenance: false },
    availability: {
      responseTimeSeries,
      period: "7d",
      bucketUnit: "day",
    },
  };
}

async function mockStatusPage(page: Page, responseTimeSeries: unknown[]) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...basePayload(),
        sections: [
          {
            uid: "55555555-5555-5555-5555-555555555555",
            name: "Core",
            slug: "core",
            position: 0,
            resources: [resourceWithSeries(responseTimeSeries)],
          },
        ],
      }),
    }),
  );
}

test.describe("Public status page — response-time chart", () => {
  test("single series renders the chart with no legend", async ({ page }) => {
    const points = [1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10),
      durationP95: 42,
      status: "up",
    }));
    await mockStatusPage(page, [{ points }]);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Response Time", { exact: true })).toBeVisible({
      timeout: 10000,
    });
    await expect(
      page.getByTestId("response-time-chart-legend"),
    ).toHaveCount(0);
  });

  test("legacy NULL-region payload (no region field) renders like single series", async ({
    page,
  }) => {
    const points = [1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10),
      durationP95: 55,
      status: "up",
    }));
    // No `region` key at all — mirrors a pre-region-tracking payload.
    await mockStatusPage(page, [{ points }]);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Response Time", { exact: true })).toBeVisible({
      timeout: 10000,
    });
    await expect(
      page.getByTestId("response-time-chart-legend"),
    ).toHaveCount(0);
  });

  test("two regions render a legend with two chips", async ({ page }) => {
    const eu2Points = [2, 1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10),
      durationP95: 40,
      status: "up",
    }));
    const us1Points = [2, 1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10 + 1),
      durationP95: 160,
      status: "up",
    }));
    await mockStatusPage(page, [
      { region: "eu2", points: eu2Points },
      { region: "us1", points: us1Points },
    ]);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const legend = page.getByTestId("response-time-chart-legend");
    await expect(legend).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("response-time-chart-legend-item")).toHaveCount(2);
    await expect(legend).toContainText("eu2");
    await expect(legend).toContainText("us1");
  });

  test("two-region legend stays compact on a mobile viewport", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 375, height: 812 });

    const eu2Points = [1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10),
      durationP95: 40,
      status: "up",
    }));
    const us1Points = [1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10 + 1),
      durationP95: 160,
      status: "up",
    }));
    await mockStatusPage(page, [
      { region: "eu2", points: eu2Points },
      { region: "us1", points: us1Points },
    ]);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const legend = page.getByTestId("response-time-chart-legend");
    await expect(legend).toBeVisible({ timeout: 10000 });

    // The legend must not force the page wider than the mobile viewport.
    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(375);
  });
});
