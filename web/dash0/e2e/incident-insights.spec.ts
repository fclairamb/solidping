import type { Page } from "@playwright/test";

import { test, expect, DASH_BASE } from "./fixtures";

/**
 * Spec 2026-11-13-01: the incident insights page (top failing checks, time to
 * acknowledge, time to resolve), reached from the incidents list.
 *
 * The first test runs against the real seeded org. The others mock the
 * insights endpoint so they can assert exact numbers and the empty state.
 */

const emptyDuration = { medianSeconds: null, p90Seconds: null, sampleCount: 0 };
const emptyResolution = { count: 0, medianSeconds: null, p90Seconds: null };

function insightsBody(period: string, incidentCount: number) {
  return {
    period,
    windowStart: new Date(Date.now() - 86_400_000).toISOString(),
    windowEnd: new Date().toISOString(),
    truncated: false,
    summary: {
      incidentCount,
      pagedCount: incidentCount,
      acknowledgedCount: 0,
      ackRate: 0,
      timeToAcknowledge: emptyDuration,
      timeToResolve:
        incidentCount > 0
          ? { medianSeconds: 600, p90Seconds: 900, sampleCount: incidentCount }
          : emptyDuration,
      byResolution: {
        auto:
          incidentCount > 0
            ? { count: incidentCount, medianSeconds: 600, p90Seconds: 900 }
            : emptyResolution,
        manual: emptyResolution,
        other: emptyResolution,
      },
    },
    checks:
      incidentCount > 0
        ? [
            {
              checkUid: "99999999-9999-9999-9999-999999999991",
              checkName: "Acme API",
              checkSlug: "acme-api",
              incidentCount,
              downtimeSeconds: 1200,
              relapseCount: 1,
              lastIncidentAt: new Date().toISOString(),
            },
          ]
        : [],
  };
}

async function mockInsights(page: Page, countFor: (period: string) => number) {
  await page.route("**/api/v1/orgs/*/incidents/insights*", async (route) => {
    const period =
      new URL(route.request().url()).searchParams.get("period") ?? "30d";
    await route.fulfill({ json: insightsBody(period, countFor(period)) });
  });
}

test.describe("Incident insights", () => {
  test("lists a failing check with its incident count, reached from the incidents list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`${DASH_BASE}/orgs/test/incidents`);
    await page.getByTestId("incidents-insights-button").click();
    await expect(page).toHaveURL(/\/incidents\/insights/);

    await page.getByTestId("insights-period-90d").click();
    await expect(page).toHaveURL(/period=90d/);

    const row = page.getByTestId("insights-check-row").first();
    await expect(row).toBeVisible({ timeout: 15000 });
    const count = Number(
      await row.getByTestId("insights-incident-count").innerText(),
    );
    expect(count).toBeGreaterThanOrEqual(1);
    await expect(page.getByTestId("insights-card-ack")).toBeVisible();
    await expect(page.getByTestId("insights-card-resolve")).toBeVisible();
    await expect(page.getByTestId("insights-card-resolution")).toBeVisible();
  });

  test("switching the period updates the URL and the numbers", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockInsights(page, (period) => (period === "7d" ? 2 : 5));

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);
    await expect(page.getByTestId("insights-incident-count")).toHaveText("5");
    // No acknowledged incident: a dash and the reason, never "0s".
    await expect(page.getByTestId("insights-card-ack")).toContainText(
      "No acknowledged incident",
    );

    await page.getByTestId("insights-period-7d").click();
    await expect(page).toHaveURL(/period=7d/);
    await expect(page.getByTestId("insights-incident-count")).toHaveText("2");
  });

  test("an org with no incident shows the empty state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockInsights(page, () => 0);

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);
    await expect(page.getByTestId("insights-empty")).toBeVisible();
    await expect(page.getByTestId("insights-check-row")).toHaveCount(0);
  });

  test("a phone viewport has no horizontal page scroll", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });
    await mockInsights(page, () => 5);

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights`);
    await expect(page.getByTestId("insights-check-row")).toBeVisible();

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow).toBe(false);
  });

  // No page.route: the page talks to the real insights endpoint. The test-mode
  // seed (server/test/testdata) has an active check incident on "Captured
  // Check" that stays open for the whole run.
  test("against the real endpoint, a seeded incident shows its check and count", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`${DASH_BASE}/orgs/test/incidents/insights?period=30d`);

    const row = page.getByTestId("insights-check-row").filter({
      has: page.getByRole("link", { name: "Captured Check" }),
    });
    await expect(row).toHaveCount(1);
    await expect(row.getByTestId("insights-incident-count")).toHaveText(
      /^[1-9]\d*$/,
    );
    await expect(page.getByTestId("insights-empty")).toHaveCount(0);
  });
});
