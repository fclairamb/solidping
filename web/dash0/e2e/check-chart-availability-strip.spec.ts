import { test, expect, mockSloCoverage } from "./fixtures";
import type { Page } from "@playwright/test";

/**
 * Spec 2026-08-26-10 — the availability strip aligned to the response-time
 * chart:
 * - the strip renders one cell per bucket under the chart, coloured from the
 *   server's status, with a distinct gray cell for a no-data bucket;
 * - the header carries the whole-window figure on every range;
 * - changing the range re-buckets (day → 1h, week → 6h, month → 1d);
 * - a drag-zoom re-buckets onto the selected window;
 * - the region filter scopes the strip exactly like the chart;
 * - below the 3h floor no strip is drawn, only the header figure.
 *
 * The buckets endpoint is stubbed so the assertions are about the CLIENT's
 * wiring (window, bucket width, region) rather than about whatever data the
 * dev database happens to hold.
 */

const HOUR_MS = 3_600_000;

interface BucketQuery {
  from: string;
  to: string;
  bucket: string | null;
  region: string | null;
}

function buildResults() {
  const now = Date.now();
  const points = [];
  const count = 120;
  const intervalMs = 12 * 60_000; // 12 min → ~24h span
  for (let i = 0; i < count; i++) {
    points.push({
      uid: `raw-${i}`,
      durationMs: 40 + (i % 5) * 8,
      status: "up",
      periodStart: new Date(now - (count - i) * intervalMs).toISOString(),
      periodType: "raw",
      region: i % 3 === 0 ? "us-east" : "eu-west",
    });
  }
  return points;
}

/** A deterministic 6-cell response: 4 up, 1 down, 1 no-data. */
function bucketsBody(bucketSeconds: number) {
  const width = bucketSeconds * 1000;
  const now = Date.now();
  const cells = Array.from({ length: 6 }, (_, i) => {
    const start = new Date(now - (6 - i) * width);
    const noData = i === 5;
    const bad = i === 3;
    const total = noData ? 0 : 60;
    const successful = noData ? 0 : bad ? 20 : 60;
    return {
      periodStart: start.toISOString(),
      periodEnd: new Date(start.getTime() + width).toISOString(),
      hasData: !noData,
      availabilityPct: noData ? null : (successful / total) * 100,
      totalChecks: total,
      successfulChecks: successful,
      status: noData ? "noData" : bad ? "down" : "up",
    };
  });
  return {
    data: cells,
    window: {
      periodStart: new Date(now - 6 * width).toISOString(),
      periodEnd: new Date(now).toISOString(),
      hasData: true,
      availabilityPct: 86.67,
      totalChecks: 300,
      successfulChecks: 260,
      status: "down",
    },
    bucketSeconds,
    windowStart: new Date(now - 6 * width).toISOString(),
    windowEnd: new Date(now).toISOString(),
  };
}

async function gotoCheckDetail(page: Page): Promise<BucketQuery[]> {
  await mockSloCoverage(page);

  const bucketQueries: BucketQuery[] = [];
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

  await page.route(
    "**/api/v1/orgs/*/checks/*/availability/buckets*",
    (route) => {
      const url = new URL(route.request().url());
      const bucket = url.searchParams.get("bucket");
      bucketQueries.push({
        from: url.searchParams.get("from") ?? "",
        to: url.searchParams.get("to") ?? "",
        bucket,
        region: url.searchParams.get("region"),
      });
      // Mirror the server: an omitted bucket means "you choose".
      const seconds = bucket ? parseInt(bucket, 10) * 3600 : 3600;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(bucketsBody(seconds)),
      });
    },
  );

  await page.route("**/api/v1/orgs/*/incidents*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
    }),
  );

  await page
    .getByTestId("app-sidebar")
    .getByRole("link", { name: "Checks" })
    .click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");

  await page.getByTestId("check-name-input").fill(`E2E Strip ${Date.now()}`);
  await page
    .getByTestId("check-url-input")
    .fill("https://example.com/strip-test");
  await page.getByTestId("check-submit-button").click();

  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
  await expect(page.locator(".recharts-wrapper")).toBeVisible({
    timeout: 15000,
  });

  return bucketQueries;
}

const cells = (page: Page) =>
  page.getByTestId("response-time-chart-availability-strip-cell");

test.describe("Chart availability strip", () => {
  test("renders one cell per bucket with a gray no-data cell, plus a header figure", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoCheckDetail(page);

    await expect(cells(page)).toHaveCount(6, { timeout: 10000 });

    // The stub's shape: 5 measured cells (one of them red) and one no-data.
    await expect(cells(page).nth(3)).toHaveAttribute("data-status", "down");
    await expect(cells(page).nth(5)).toHaveAttribute("data-status", "noData");
    // A no-data cell must not claim a percentage — that is the third state.
    await expect(cells(page).nth(5)).toHaveAttribute(
      "aria-label",
      /No data/,
    );

    const header = page.getByTestId("response-time-chart-window-availability");
    await expect(header).toBeVisible();
    await expect(header).toContainText("86.7%");
  });

  test("hovering a cell reveals its exact percentage, span and probe counts", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoCheckDetail(page);

    await expect(cells(page)).toHaveCount(6, { timeout: 10000 });

    // The bad cell: 20 of 60 probes up. Tooltips are matched by CONTENT rather
    // than by position, because Radix leaves the previous tooltip mounted for a
    // moment after the pointer moves on — `.first()` would then read the cell
    // we just left.
    await cells(page).nth(3).hover();

    const tooltip = page.getByRole("tooltip").filter({ hasText: "20 / 60" });
    await expect(tooltip).toBeVisible({ timeout: 5000 });
    await expect(tooltip).toContainText("33.3%");
    await expect(tooltip).toContainText("Down");

    // The no-data cell must offer no percentage at all — a tooltip claiming a
    // number for a bucket with no probes is the fabricated figure this spec
    // exists to remove.
    // Leave the current cell, then hover the next one TWICE: Radix opens on a
    // pointermove inside the trigger, and the single move Playwright's first
    // hover() emits lands on the boundary often enough to be flaky.
    await page.mouse.move(0, 0);
    await cells(page).nth(5).hover();
    await cells(page).nth(5).hover();

    const noDataTooltip = page
      .getByRole("tooltip")
      .filter({ hasText: "No data" });
    await expect(noDataTooltip).toBeVisible({ timeout: 5000 });
    expect(await noDataTooltip.textContent()).not.toContain("%");
  });

  test("moving from one cell to the next swaps the tooltip instead of keeping the previous period", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoCheckDetail(page);

    await expect(cells(page)).toHaveCount(6, { timeout: 10000 });

    // Cell 3 is the bad bucket (20 / 60), cell 4 is a healthy one (60 / 60) —
    // adjacent, so a pointer sweep along the strip crosses straight from one
    // to the other.
    await cells(page).nth(3).hover();
    await expect(
      page.getByRole("tooltip").filter({ hasText: "20 / 60" }),
    ).toBeVisible({ timeout: 5000 });

    // Move straight onto the neighbour, with no detour off the strip and no
    // second nudge. Radix's hoverable-content grace area used to make this the
    // failing case: leaving an open cell sets a POINTER-IN-TRANSIT flag on the
    // shared TooltipProvider, and while it is set every other trigger's
    // onPointerMove is a no-op — so the pointer would land on cell 4 while the
    // popup still showed cell 3's period. `disableHoverableContent` on the
    // cell tooltip removes that grace area (see components/ui/availability-strip.tsx).
    await cells(page).nth(4).hover();

    await expect(
      page.getByRole("tooltip").filter({ hasText: "60 / 60" }),
    ).toBeVisible({ timeout: 5000 });
    // ...and the period we left must be gone, not merely covered.
    await expect(
      page.getByRole("tooltip").filter({ hasText: "20 / 60" }),
    ).toHaveCount(0, { timeout: 5000 });
  });

  test("re-buckets when the chart range changes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const queries = await gotoCheckDetail(page);

    // Day is the default range → 1h cells.
    await expect
      .poll(() => queries.at(-1)?.bucket, { timeout: 10000 })
      .toBe("1h");

    await page.getByRole("button", { name: "Week", exact: true }).click();
    await expect.poll(() => queries.at(-1)?.bucket, { timeout: 10000 }).toBe(
      "6h",
    );

    await page.getByRole("button", { name: "Month", exact: true }).click();
    await expect.poll(() => queries.at(-1)?.bucket, { timeout: 10000 }).toBe(
      "24h",
    );
  });

  test("hides the strip below the 3h floor but keeps the header figure", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoCheckDetail(page);

    await expect(cells(page)).toHaveCount(6, { timeout: 10000 });

    await page.getByRole("button", { name: "Hour", exact: true }).click();

    await expect(cells(page)).toHaveCount(0, { timeout: 10000 });
    await expect(
      page.getByTestId("response-time-chart-window-availability"),
    ).toBeVisible();
  });

  test("re-buckets onto a drag-zoom window", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const queries = await gotoCheckDetail(page);

    // Start from the month range so the zoom's narrower bucket is a real change
    // (1d → 1h), not a coincidence of already being at the floor.
    await page.getByRole("button", { name: "Month", exact: true }).click();
    await expect.poll(() => queries.at(-1)?.bucket, { timeout: 10000 }).toBe(
      "24h",
    );

    const surface = page.locator(".recharts-wrapper").first();
    const box = await surface.boundingBox();
    if (!box) throw new Error("chart has no bounding box");
    const y = box.y + box.height / 2;
    const startX = box.x + box.width * 0.3;
    const endX = box.x + box.width * 0.7;

    await page.mouse.move(startX, y);
    await page.mouse.down();
    for (let i = 1; i <= 6; i++) {
      await page.mouse.move(startX + ((endX - startX) * i) / 6, y);
    }
    await page.mouse.up();

    await page.waitForURL(/graphFrom=/, { timeout: 5000 });

    // The zoom window replaces the range's window AND its bucket width.
    await expect
      .poll(
        () => {
          const last = queries.at(-1);
          if (!last) return null;
          const spanMs = Date.parse(last.to) - Date.parse(last.from);
          return spanMs > 0 && spanMs < 30 * 24 * HOUR_MS;
        },
        { timeout: 10000 },
      )
      .toBe(true);
    expect(queries.at(-1)?.bucket).not.toBe("24h");
  });

  test("scopes the strip to the chart's region filter", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const queries = await gotoCheckDetail(page);

    // No region chip selected → the strip sums every region.
    await expect
      .poll(() => queries.at(-1)?.region, { timeout: 10000 })
      .toBe(null);

    const chip = page.getByTestId("response-time-chart-region-chip-us-east");
    await expect(chip).toBeVisible({ timeout: 10000 });
    await chip.click();

    // The chart and the strip must describe the SAME region — that they did not
    // is the defect this spec exists to fix.
    await expect
      .poll(() => queries.at(-1)?.region, { timeout: 10000 })
      .toBe("us-east");
    await expect(page).toHaveURL(/region=us-east/);
  });
});
