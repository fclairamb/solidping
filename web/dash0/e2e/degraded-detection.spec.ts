import { test, expect, mockSloCoverage, type Page } from "./fixtures";

// Coverage for spec 2026-09-22-03: the amber band the chart draws over a
// degraded episode. Spec 2026-09-24-08 removed the dry run, and with it the
// check-page banner and the checks list filter this file used to cover.
//
// Mocked at the API boundary like checks-index-status-type-filters.spec.ts: a
// real degraded episode needs 60 probes and a minute-resolution evaluator sweep,
// which is not something an e2e run can produce deterministically. What is under
// test here is the UI's reading of the API's answer.

interface MockCheck {
  uid: string;
  name: string;
  degradedEnabled: boolean;
}

const ORG = "test";

/**
 * The episode is anchored to NOW, not to a fixed calendar date: recharts draws a
 * ReferenceArea against the chart's live time domain (the default view is the
 * last 24 h), and a band outside that domain is legitimately not drawn. A fixed
 * 2026-09-22 window silently tested nothing the day after.
 */
const EPISODE_END_MS = Date.now() - 30 * 60_000;
const EPISODE_START_MS = EPISODE_END_MS - 53 * 60_000;
const EPISODE_START = new Date(EPISODE_START_MS).toISOString();
const EPISODE_END = new Date(EPISODE_END_MS).toISOString();

/**
 * The chart renders no plot at all — and therefore no band — until it has
 * results, so a spec that asserts on the band has to supply them. 24 h of
 * successful probes at 12-minute spacing, the same shape check-chart-zoom.spec.ts
 * uses.
 */
function buildResults() {
  const now = Date.now();
  const count = 120;
  const intervalMs = 12 * 60_000;

  return Array.from({ length: count }, (_, index) => ({
    uid: `degraded-raw-${index}`,
    durationMs: 40 + (index % 5) * 8,
    status: "up",
    periodStart: new Date(now - (count - index) * intervalMs).toISOString(),
    periodType: "raw",
  }));
}

/** Mocks the results endpoint the chart plots, plus the SLO coverage chip. */
async function mockChartData(page: Page): Promise<void> {
  await mockSloCoverage(page);

  const points = buildResults();

  await page.route("**/api/v1/orgs/*/results*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: points,
        pagination: { total: points.length, size: points.length },
      }),
    }),
  );
}

async function mockChecksList(page: Page, checks: MockCheck[]): Promise<void> {
  await page.route("**/api/v1/orgs/*/check-groups*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );

  await page.route("**/api/v1/orgs/*/check-types*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          { type: "http", description: "http", labels: [], enabled: true },
        ],
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/escalation-policies*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );

  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (
      !url.includes("/checks") ||
      url.includes("/check-groups") ||
      url.includes("/check-types")
    ) {
      return route.continue();
    }

    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: checks.map((check) => ({
          uid: check.uid,
          name: check.name,
          slug: check.uid,
          type: "http",
          enabled: true,
          status: "up",
          period: "00:01:00",
          config: { url: `https://example.com/${check.uid}` },
          degradedFailures: 5,
          degradedFailuresWindow: 60,
          degradedSlow: 3,
          degradedSlowWindow: 6,
          slowThresholdMs: 0,
          degradedEnabled: check.degradedEnabled,
        })),
        pagination: { total: checks.length },
      }),
    });
  });
}

/** Mocks one check's detail page, its incidents and its (empty) results. */
async function mockCheckDetail(
  page: Page,
  check: MockCheck,
  opts: { degradedIncident: boolean; startedAt?: string; resolvedAt?: string },
): Promise<void> {
  await page.route(`**/api/v1/orgs/*/checks/${check.uid}*`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        uid: check.uid,
        name: check.name,
        slug: check.uid,
        type: "http",
        enabled: true,
        status: "up",
        period: "00:01:00",
        regions: [],
        config: { url: `https://example.com/${check.uid}` },
        degradedFailures: 5,
        degradedFailuresWindow: 60,
        degradedSlow: 3,
        degradedSlowWindow: 6,
        slowThresholdMs: 0,
        degradedEnabled: check.degradedEnabled,
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/incidents*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.degradedIncident
          ? [
              {
                uid: "e2e-degraded-incident",
                number: 41,
                checkUid: check.uid,
                kind: "degraded",
                state: "resolved",
                startedAt: opts.startedAt ?? EPISODE_START,
                resolvedAt: opts.resolvedAt ?? EPISODE_END,
                title: `${check.name} is degraded: 7 failures in the last 60 probes (93.8%)`,
                failureCount: 7,
                relapseCount: 0,
              },
            ]
          : [],
        pagination: { total: opts.degradedIncident ? 1 : 0 },
      }),
    }),
  );
}

test.describe("Degraded detection — dashboard surfaces", () => {
  test("the chart shades a degraded episode", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const check: MockCheck = {
      uid: "e2e-degraded-band",
      name: "Degraded Band Check",
      degradedEnabled: true,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, { degradedIncident: true });
    await mockChartData(page);

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    // A band, not dots: isolated red dots over an hour do not read as an event.
    await expect(
      page.locator('[data-testid="chart-degraded-span"]').first(),
    ).toBeAttached();
  });

  test("a check with no degraded episode gets no band", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    // Negative control for the band test above: same chart, same data, no
    // degraded incident — so a band that rendered unconditionally would fail
    // here instead of passing both ways.
    const check: MockCheck = {
      uid: "e2e-degraded-no-band",
      name: "Degraded No Band Check",
      degradedEnabled: true,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, { degradedIncident: false });
    await mockChartData(page);

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.locator('[data-testid="chart-degraded-span"]'),
    ).toHaveCount(0);
  });

  test("an episode that began before the visible window still shades its overlap", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    // recharts defaults a ReferenceArea to ifOverflow="discard", which drops the
    // element outright when one edge falls outside the domain. An hour-long
    // episode that started before a 24 h view is the COMMON case, and dropping
    // it means the operator sees isolated dots again — the exact failure this
    // feature exists to fix. The chart passes ifOverflow="hidden" so the overlap
    // is drawn, clipped to the plot area.
    const check: MockCheck = {
      uid: "e2e-degraded-overflow",
      name: "Degraded Overflow Check",
      degradedEnabled: true,
    };

    await mockChecksList(page, [check]);
    await mockCheckDetail(page, check, {
      degradedIncident: true,
      // Starts 36 h back — outside the default 24 h domain — and ends inside it.
      startedAt: new Date(Date.now() - 36 * 60 * 60_000).toISOString(),
      resolvedAt: new Date(Date.now() - 60 * 60_000).toISOString(),
    });
    await mockChartData(page);

    await page.goto(`orgs/${ORG}/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.locator('[data-testid="chart-degraded-span"]').first(),
    ).toBeAttached();
  });
});
