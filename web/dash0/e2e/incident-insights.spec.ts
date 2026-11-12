import type { Page, Route } from "@playwright/test";

import { test, expect, DASH_BASE } from "./fixtures";

/**
 * Incident insights page (spec 2026-11-13-01): top failing checks and the
 * time to acknowledge / resolve cards, period in the URL, empty state.
 * The insights endpoint is mocked so the numbers are the page's to render.
 */

const CHECK_UID = "88888888-8888-8888-8888-888888888881";

const stats = (median: number | null, p90: number | null, sampleCount: number) => ({
  medianSeconds: median,
  p90Seconds: p90,
  sampleCount,
});

function payload(period: string, incidentCount: number) {
  return {
    period,
    truncated: false,
    summary: {
      incidentCount,
      pagedCount: incidentCount,
      acknowledgedCount: incidentCount > 0 ? 1 : 0,
      ackRate: incidentCount > 0 ? 1 / incidentCount : 0,
      timeToAcknowledge: incidentCount > 0 ? stats(120, 120, 1) : stats(null, null, 0),
      timeToResolve: stats(600, 900, incidentCount),
      byResolution: {
        auto: { count: incidentCount, medianSeconds: 600, p90Seconds: 900 },
        manual: { count: 0, medianSeconds: null, p90Seconds: null },
        other: { count: 0, medianSeconds: null, p90Seconds: null },
      },
    },
    checks:
      incidentCount > 0
        ? [
            {
              checkUid: CHECK_UID,
              name: "Acme API",
              slug: "acme-api",
              incidentCount,
              downtimeSeconds: 1800,
              relapseCount: 1,
              lastIncidentAt: new Date().toISOString(),
            },
          ]
        : [],
  };
}

async function mockInsights(page: Page, counts: Record<string, number>) {
  await page.route("**/api/v1/orgs/*/incidents/insights**", (route: Route) => {
    const period = new URL(route.request().url()).searchParams.get("period") ?? "30d";
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload(period, counts[period] ?? 0)),
    });
  });
}

test.describe("Incident insights", () => {
  test("shows the failing check and its incident count", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await mockInsights(page, { "30d": 3, "7d": 1 });

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);

    const row = page.getByTestId("insights-check-row");
    await expect(row).toHaveCount(1);
    await expect(row).toContainText("Acme API");
    await expect(row.getByTestId("insights-check-incidents-link")).toHaveText("3");
    await expect(page.getByTestId("insights-ack-card")).toContainText("1 of 3 acknowledged");
    await expect(page.getByTestId("insights-resolve-card")).toContainText("10m 0s");
  });

  test("switching the period updates the URL and the numbers", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await mockInsights(page, { "30d": 3, "7d": 1 });

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);
    await expect(page.getByTestId("insights-check-incidents-link")).toHaveText("3");

    await page.getByTestId("insights-period-select").click();
    await page.getByTestId("insights-period-7d").click();

    await expect(page).toHaveURL(/period=7d/);
    await expect(page.getByTestId("insights-check-incidents-link")).toHaveText("1");
  });

  test("an org with no incident shows the empty state", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await mockInsights(page, {});

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);

    await expect(page.getByTestId("insights-empty")).toBeVisible();
    await expect(page.getByTestId("insights-check-row")).toHaveCount(0);
  });

  test("a mobile viewport has no horizontal page scroll", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 800 });
    await mockInsights(page, { "30d": 3 });

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);
    await expect(page.getByTestId("insights-check-row")).toBeVisible();

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
});
