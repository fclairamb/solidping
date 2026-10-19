import { test, expect, API_BASE, mockSloCoverage, type Page } from "./fixtures";

// Spec 2026-10-12-08: HTTP results record a timing breakdown. The result page
// shows a Timing card, and the check page has a Timing chart view that fetches
// the metrics with its own query. Results are not creatable through the public
// API, so the results endpoints are mocked; the check itself is real.

const METRICS = {
  dns_time_ms: 12.4,
  connect_time_ms: 31.2,
  tls_time_ms: 58.9,
  ttfb_time_ms: 140.3,
};

async function createHttpCheck(page: Page): Promise<string> {
  const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const token = (await login.json()).accessToken;
  const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      type: "http",
      name: `E2E Timing ${Date.now()}`,
      config: { url: "https://example.com/timing" },
      period: "00:05:00",
    },
  });
  expect(created.status()).toBe(201);
  return (await created.json()).uid;
}

test.describe("HTTP timing breakdown", () => {
  test("the result page shows the timing card with only the phases that ran", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checkUid = await createHttpCheck(page);
    const resultUid = "cccccccc-0000-7000-8000-0000000000aa";

    await page.route(`**/api/v1/orgs/*/checks/*/results/${resultUid}`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid: resultUid,
          status: "up",
          durationMs: 250,
          periodStart: "2026-10-12T10:00:00Z",
          periodType: "raw",
          metrics: METRICS,
        }),
      }),
    );

    await page.goto(`orgs/test/checks/${checkUid}/results/${resultUid}`);

    await expect(page.getByTestId("http-timing-card")).toBeVisible();
    for (const phase of ["dns", "connect", "tls", "ttfb"]) {
      await expect(page.getByTestId(`http-timing-phase-${phase}`)).toBeVisible();
    }
    // Positive control above; these phases did not run.
    await expect(page.getByTestId("http-timing-phase-download")).toHaveCount(0);
    await expect(page.getByTestId("http-timing-phase-redirect")).toHaveCount(0);
  });

  test("selecting the Timing view writes the search param and renders the chart", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checkUid = await createHttpCheck(page);
    await mockSloCoverage(page);

    const requestedWith: string[] = [];
    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = new URL(route.request().url());
      requestedWith.push(url.searchParams.get("with") ?? "");
      const now = Date.now();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [30, 20, 10].map((minutes, i) => ({
            uid: `dddddddd-0000-7000-8000-00000000000${i}`,
            status: "up",
            durationMs: 250,
            periodStart: new Date(now - minutes * 60_000).toISOString(),
            periodType: "raw",
            metrics: METRICS,
          })),
          pagination: { total: 3, size: 3 },
        }),
      });
    });

    await page.goto(`orgs/test/checks/${checkUid}`);
    await expect(page.getByTestId("chart-view-timing")).toBeVisible();
    await expect(page.getByTestId("timing-chart-card")).toHaveCount(0);

    // Default view stays blob-free.
    expect(requestedWith.length).toBeGreaterThan(0);
    for (const w of requestedWith) expect(w).not.toContain("metrics");

    await page.getByTestId("chart-view-timing").click();
    await expect(page).toHaveURL(/graphView=timing/);
    await expect(page.getByTestId("timing-chart-card")).toBeVisible();
    await expect(page.getByTestId("timing-chart")).toBeVisible();
    // One view switch (the page-level one), not a second one inside the card.
    await expect(page.getByTestId("chart-view-timing")).toHaveCount(1);
    // The phases are named in a legend.
    await expect(
      page.getByTestId("timing-chart").locator(".recharts-legend-item"),
    ).toHaveCount(6);
    expect(requestedWith.some((w) => w.includes("metrics"))).toBe(true);
  });
});
