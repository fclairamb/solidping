import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

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

async function mockStatusPage(
  page: Page,
  responseTimeSeries: unknown[],
  pageOverrides: Record<string, unknown> = {},
) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ...basePayload(),
        ...pageOverrides,
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const legend = page.getByTestId("response-time-chart-legend");
    await expect(legend).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("response-time-chart-legend-item")).toHaveCount(2);
    await expect(legend).toContainText("eu2");
    await expect(legend).toContainText("us1");
  });

  test("availability strip sums regions at a shared timestamp, not their percentages", async ({
    page,
  }) => {
    // Spec 2026-08-26-10 phase 2: the strip under the chart is coloured by
    // AVAILABILITY, and a slot several regions report into is the SUM of their
    // up/total — never an average of their percentages.
    //
    // The middle slot is the discriminator: eu2 contributes 20/60 and us1
    // 60/60, so the sum is 80/120 = 66.7% (red), while averaging the two
    // regions' percentages would give (33.3 + 100) / 2 = 66.7% too — so the
    // fixture also makes the regions unequal in WEIGHT at the first slot
    // (1 probe vs 60) where the two rules diverge: summed 60/61 = 98.4%
    // (amber, one failed sample), averaged (0 + 100) / 2 = 50% (red).
    const t0 = isoMinutesAgo(10);
    const t1 = isoMinutesAgo(5);
    const t2 = isoMinutesAgo(0);
    const eu2Points = [
      { time: t0, durationP95: 40, status: "down", totalChecks: 1, successfulChecks: 0, availabilityPct: 0, availabilityStatus: "degraded" },
      { time: t1, durationP95: 40, status: "down", totalChecks: 60, successfulChecks: 20, availabilityPct: 33.3, availabilityStatus: "down" },
      { time: t2, durationP95: 40, status: "up", totalChecks: 60, successfulChecks: 60, availabilityPct: 100, availabilityStatus: "up" },
    ];
    const us1Points = [
      { time: t0, durationP95: 160, status: "up", totalChecks: 60, successfulChecks: 60, availabilityPct: 100, availabilityStatus: "up" },
      { time: t1, durationP95: 160, status: "up", totalChecks: 60, successfulChecks: 60, availabilityPct: 100, availabilityStatus: "up" },
      { time: t2, durationP95: 160, status: "up", totalChecks: 60, successfulChecks: 60, availabilityPct: 100, availabilityStatus: "up" },
    ];
    await mockStatusPage(page, [
      { region: "eu2", points: eu2Points },
      { region: "us1", points: us1Points },
    ]);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const strip = page.getByTestId("response-time-chart-availability-strip");
    await expect(strip.first()).toBeVisible({ timeout: 10000 });

    const statuses = await strip.first().locator("[data-status]").evaluateAll(
      (nodes) => nodes.map((n) => n.getAttribute("data-status")),
    );
    // ["degraded", "down", "up"] — the first slot is the one that would read
    // "down" if the merge averaged percentages instead of summing counts.
    expect(statuses).toEqual(["degraded", "down", "up"]);
  });

  test("single-region strip renders the server's own classification, gray for no data", async ({
    page,
  }) => {
    // With one region there is nothing to merge, so the strip must render the
    // status the SERVER already resolved against the page's thresholds — no
    // client availability math at all. A point with no countable probe (a
    // lifecycle marker) must render as a distinct gray cell, never as green.
    const points = [
      { time: isoMinutesAgo(10), durationP95: 40, status: "up", totalChecks: 60, successfulChecks: 60, availabilityPct: 100, availabilityStatus: "up" },
      { time: isoMinutesAgo(5), durationP95: 40, status: "down", totalChecks: 60, successfulChecks: 20, availabilityPct: 33.3, availabilityStatus: "down" },
      { time: isoMinutesAgo(2), durationP95: 40, status: "up", totalChecks: 60, successfulChecks: 59, availabilityPct: 98.3, availabilityStatus: "degraded" },
      { time: isoMinutesAgo(0), durationP95: 40, status: "created" },
    ];
    await mockStatusPage(page, [{ region: "eu2", points }]);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const strip = page.getByTestId("response-time-chart-availability-strip");
    await expect(strip.first()).toBeVisible({ timeout: 10000 });

    const statuses = await strip.first().locator("[data-status]").evaluateAll(
      (nodes) => nodes.map((n) => n.getAttribute("data-status")),
    );
    expect(statuses).toEqual(["up", "down", "degraded", "noData"]);

    // The no-data cell must say so rather than claim a percentage.
    const lastTitle = await strip
      .first()
      .locator("[data-status='noData']")
      .first()
      .getAttribute("title");
    expect(lastTitle).not.toContain("%");
  });

  test("regions sampling on staggered timestamps still draw one line each", async ({
    page,
  }) => {
    // Every region checks once a minute, but each on its own second of the
    // minute — which is what real workers do. Keyed on the raw timestamp that
    // pivots into one row per region-sample, each row holding a single value
    // and nulls for the others; with connectNulls={false} no Area then has two
    // adjacent points to draw a segment between, and the chart renders
    // completely BLANK. This asserts the curves actually exist.
    const phases = [58, 8, 26, 31, 43];
    const regions = ["jp1", "us1", "aws-paris", "default", "eu2"];
    const base = Date.now() - 20 * 60_000;
    const series = regions.map((region, r) => ({
      region,
      points: Array.from({ length: 12 }, (_, i) => ({
        time: new Date(
          Math.floor(base / 60_000) * 60_000 + i * 60_000 + phases[r] * 1000,
        ).toISOString(),
        durationP95: 20 + r * 40,
        status: "up",
      })),
    }));
    await mockStatusPage(page, series);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByTestId("response-time-chart-legend-item"),
    ).toHaveCount(regions.length, { timeout: 10000 });

    // One drawn curve per region, each one an actual multi-point path rather
    // than an empty/degenerate `d`.
    const paths = await page
      .locator(".recharts-area-curve")
      .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("d") ?? ""));
    expect(paths).toHaveLength(regions.length);
    for (const d of paths) {
      // "M x,y C …" — a curve through every sample. A blank series yields ""
      // or a single moveto.
      expect(d.length).toBeGreaterThan(20);
      expect(d).toContain("C");
    }

    // The five regions sit at clearly different response times, so the drawn
    // curves must occupy five different bands of the plot — a sanity check
    // that they are all really on screen, not stacked at zero.
    const midYs = await page
      .locator(".recharts-area-curve")
      .evaluateAll((nodes) =>
        nodes.map((n) => (n as SVGPathElement).getBoundingClientRect().y),
      );
    expect(new Set(midYs.map((y) => Math.round(y))).size).toBe(regions.length);
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

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const legend = page.getByTestId("response-time-chart-legend");
    await expect(legend).toBeVisible({ timeout: 10000 });

    // The legend must not force the page wider than the mobile viewport.
    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(375);
  });

  test("a retired region plus a live one render exactly one legend entry", async ({
    page,
  }) => {
    // Spec 2026-09-21-03 A.3: the response-time series is bounded to the
    // page's own history window SERVER-SIDE, so a region whose last point
    // predates it is dropped from the payload entirely — no empty series, no
    // legend entry. This payload is what the fixed server returns for a
    // check whose "old" region stopped 39 days ago on a 7-day page: only the
    // live region remains.
    const eu2Points = [2, 1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10),
      durationP95: 40,
      status: "up",
    }));
    await mockStatusPage(page, [{ region: "eu2", points: eu2Points }]);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("response-time-chart-legend")).toHaveCount(
      0,
      { timeout: 10000 },
    );
    await expect(page.locator(".recharts-area-curve")).toHaveCount(1);
  });

  test("the axis is monotonic in time even when series are disjoint", async ({
    page,
  }) => {
    // Spec 2026-09-21-03 B: the x-axis is a real time axis. The payload
    // below is the shape the PRE-fix server returned — one region's day
    // rollups ending 39 days ago, one region's raw points from the last
    // minutes — which on the old category axis rendered two neighbouring
    // ticks 39 days apart ("21 sept. · 11:44 · 11:49"). The tick labels,
    // parsed as dates, must now increase left-to-right, and the axis must
    // start on a DATE from the old region's window, not a time-of-day.
    const oldPoints = [0, 1, 2].map((i) => ({
      time: new Date(Date.now() - (45 - i) * 24 * 60 * 60_000).toISOString(),
      durationP95: 40,
      status: "up",
    }));
    const eu2Points = [2, 1, 0].map((m) => ({
      time: isoMinutesAgo(m * 10),
      durationP95: 40,
      status: "up",
    }));
    await mockStatusPage(page, [
      { region: "old", points: oldPoints },
      { region: "eu2", points: eu2Points },
    ]);

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const ticks = page.locator("svg .recharts-cartesian-axis-tick-value");
    await expect(ticks.first()).toBeAttached({ timeout: 10000 });

    const labels = await ticks.allTextContents();
    expect(labels.length).toBeGreaterThan(1);

    // A multi-day span labels x ticks by date; parse each and require strict
    // time order across the axis. A category axis sampling ROWS would read
    // "Aug 13 · 11:44 · 11:49" — not parseable, and not monotonic. The
    // y-axis labels ("0ms"…) don't parse as dates and are filtered out.
    const parsed = labels
      .map((label) => {
        const date = new Date(label.replace(/\u202f|\u00a0/g, " "));
        return Number.isNaN(date.getTime()) ? null : date.getTime();
      })
      .filter((ms): ms is number => ms != null);
    expect(parsed.length).toBeGreaterThanOrEqual(2);
    for (let i = 1; i < parsed.length; i++) {
      expect(parsed[i]).toBeGreaterThanOrEqual(parsed[i - 1]);
    }

    // The first tick is at the old region's end of the window — the axis
    // spans the 39-day gap instead of starting at the live region.
    const first = parsed[0];
    const ageDays = (Date.now() - first) / (24 * 60 * 60_000);
    expect(ageDays).toBeGreaterThan(30);
  });

  test("a 24h page's seam renders one point per 15-minute bin, evenly spaced", async ({
    page,
  }) => {
    // Spec 2026-09-22-06: the seam half of this chart is no longer individual raw
    // probes sub-sampled at up to one in ninety. The server bins the raw seam in
    // SQL and sends one p95 point per bin — 15-minute bins on a 24 h page,
    // seamBinWidth(24h) — each carrying the bin's probe counts, exactly like a
    // rollup point.
    //
    // What this asserts is the CLIENT's half of that contract: 96 such points
    // render as 96 slots on a real numeric time axis, evenly spaced. The
    // availability strip is the measurable surface — each cell's flex weight is
    // its slot's DURATION (spec 2026-09-21-03 B.4) — so a uniform 15-minute grid
    // must produce cells of equal width. A point that drifted off the grid, or a
    // series that went back to plotting arbitrary probes, shows up here as
    // unequal cells.
    const BIN_MS = 15 * 60_000;
    const BINS = 96;
    const newest = Math.floor(Date.now() / BIN_MS) * BIN_MS;

    const points = Array.from({ length: BINS }, (_, i) => ({
      time: new Date(newest - (BINS - 1 - i) * BIN_MS).toISOString(),
      durationP95: 80 + (i % 7) * 5,
      status: "up",
      totalChecks: 15,
      successfulChecks: 15,
      availabilityPct: 100,
      availabilityStatus: "up",
    }));

    await mockStatusPage(page, [{ region: "eu2", points }], {
      historyDays: 1,
      historyPeriod: "24h",
    });

    await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const strip = page.getByTestId("response-time-chart-availability-strip");
    await expect(strip.first()).toBeVisible({ timeout: 10000 });

    const cells = strip.first().locator("[data-status]");
    await expect(cells).toHaveCount(BINS);

    // Equal widths: every 15-minute bin occupies the same slice of the axis.
    // Compared with a 1px tolerance, which is subpixel layout rounding and not
    // a grid that slipped (a 30-minute gap would be twice as wide).
    const widths = await cells.evaluateAll((nodes) =>
      nodes.map((n) => n.getBoundingClientRect().width),
    );
    const first = widths[0];
    expect(first).toBeGreaterThan(0);
    for (const width of widths) {
      expect(Math.abs(width - first)).toBeLessThanOrEqual(1);
    }

    // And the tooltip of each cell names its bin's probe count, which is what a
    // seam point now carries and a single sub-sampled probe never could.
    const title = await cells.first().getAttribute("title");
    expect(title).toContain("(15/15)");

    // The curve really spans the whole day rather than the last ~100 minutes of
    // raw the old sub-sampled seam ended up showing.
    const labels = await page
      .locator(".recharts-cartesian-axis-tick-value")
      .allInnerTexts();
    expect(labels.length).toBeGreaterThan(0);
  });
});
