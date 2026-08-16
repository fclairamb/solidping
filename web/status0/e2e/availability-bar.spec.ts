import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * Public status page availability bar — segment geometry.
 *
 * The bar used to be 30 `flex-1` divs with a fixed 3px gap, which cannot space
 * evenly: 30 segments in a 686px row is 19.97px each, and box backgrounds are
 * painted on whole device pixels, so the browser kept the gaps and paid for the
 * fraction out of the bars. Measured, that put a full CSS pixel of difference
 * between segments — and a thin segment reads as extra space beside it, which
 * is the "dirty", unevenly spaced look. The bar is drawn as one SVG now, where
 * the segments keep their exact fractional geometry.
 *
 * These tests measure the rendered segments, at a fractional device pixel ratio
 * as well as a whole one, because that is where the old layout was worst.
 */
const ORG = "e2e-availability-bar";
const SLUG = "availability";
const DAYS = 30;
const EXPECTED_GAP_PX = 3;

// Sub-pixel slack for reading back getBoundingClientRect(): the container's own
// left edge can sit on a fraction, which shifts every segment equally.
const EPSILON = 0.02;

function dailyAvailability() {
  return Array.from({ length: DAYS }, (_, i) => {
    const date = new Date(Date.UTC(2026, 6, 1 + i));
    return {
      date: date.toISOString().slice(0, 10),
      time: date.toISOString(),
      availabilityPct: 100,
      status: "up",
    };
  });
}

async function mockStatusPage(page: Page) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        uid: "66666666-6666-6666-6666-666666666666",
        name: "Availability Demo Page",
        slug: SLUG,
        visibility: "public",
        isDefault: false,
        enabled: true,
        showAvailability: true,
        showResponseTime: false,
        historyDays: DAYS,
        historyPeriod: "30d",
        availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
        overallStatus: "operational",
        sections: [
          {
            uid: "77777777-7777-7777-7777-777777777777",
            name: "Core",
            slug: "core",
            position: 0,
            resources: [
              {
                uid: "88888888-8888-8888-8888-888888888888",
                checkUid: "99999999-9999-9999-9999-999999999999",
                position: 0,
                check: {
                  name: "API",
                  type: "http",
                  status: "up",
                  inMaintenance: false,
                },
                availability: {
                  dailyAvailability: dailyAvailability(),
                  overallAvailabilityPct: 99.944,
                  period: "30d",
                  bucketUnit: "day",
                  responseTimeSeries: [],
                },
              },
            ],
          },
        ],
      }),
    }),
  );
}

async function segmentBoxes(page: Page) {
  const segments = page.getByTestId("availability-bar-segment");
  await expect(segments).toHaveCount(DAYS, { timeout: 10000 });
  return segments.evaluateAll((nodes) =>
    nodes.map((n) => {
      const rect = n.getBoundingClientRect();
      return { left: rect.left, right: rect.right, width: rect.width };
    }),
  );
}

for (const viewport of [
  { name: "desktop", width: 1280, height: 800 },
  // An odd width lands the row on a fraction, which is where the old layout
  // started dropping pixels.
  { name: "desktop odd width", width: 1237, height: 800 },
  // Mobile is where the bar is tightest.
  { name: "mobile", width: 375, height: 812 },
]) {
  test(`availability bar segments are evenly spaced (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await mockStatusPage(page);

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    const boxes = await segmentBoxes(page);

    const gaps = boxes.slice(1).map((box, i) => box.left - boxes[i].right);
    for (const [i, gap] of gaps.entries()) {
      expect(
        Math.abs(gap - EXPECTED_GAP_PX),
        `gap ${i} should be ${EXPECTED_GAP_PX}px, got ${gap}`,
      ).toBeLessThan(EPSILON);
    }

    // Every segment the same width, and every step from one segment to the
    // next the same distance — the two things the old flex layout could not
    // hold at the same time.
    const widths = boxes.map((b) => b.width);
    expect(Math.max(...widths) - Math.min(...widths)).toBeLessThan(EPSILON);

    const pitches = boxes.slice(1).map((box, i) => box.left - boxes[i].left);
    expect(Math.max(...pitches) - Math.min(...pitches)).toBeLessThan(EPSILON);
  });
}

test("availability bar stays inside its container and never overflows the page", async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await mockStatusPage(page);

  await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
  await page.waitForLoadState("networkidle");

  const boxes = await segmentBoxes(page);
  const rowWidth = await page
    .getByTestId("availability-bar-segment")
    .first()
    .evaluate((n) => n.parentElement!.getBoundingClientRect().width);

  const span = boxes[boxes.length - 1].right - boxes[0].left;
  expect(span).toBeLessThanOrEqual(rowWidth + EPSILON);

  const scrollWidth = await page.evaluate(
    () => document.documentElement.scrollWidth,
  );
  expect(scrollWidth).toBeLessThanOrEqual(375);
});
