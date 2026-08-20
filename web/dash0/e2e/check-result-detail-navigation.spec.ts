import { test, expect, mockSloCoverage } from "./fixtures";

/**
 * Tests for the result detail page's Previous/Next navigation buttons
 * (spec 2026-07-05-14): mocks the results-list and single-result-detail
 * endpoints (same convention as check-chart-point-preview.spec.ts, since
 * results aren't directly creatable via the public API) with three results
 * carrying previousUid/nextUid, and drives stepping through all three,
 * confirming the region search param survives navigation.
 */

const resultUids = {
  oldest: "aaaaaaaa-0000-7000-8000-000000000001",
  middle: "aaaaaaaa-0000-7000-8000-000000000002",
  newest: "aaaaaaaa-0000-7000-8000-000000000003",
};

// One fixture per uid: only the middle result has both previousUid and
// nextUid; the boundary rows each omit the field pointing off the end.
const detailFixtures: Record<string, Record<string, unknown>> = {
  [resultUids.oldest]: {
    uid: resultUids.oldest,
    durationMs: 10,
    status: "up",
    periodStart: "2026-01-01T00:00:00Z",
    periodType: "raw",
    nextUid: resultUids.middle,
  },
  [resultUids.middle]: {
    uid: resultUids.middle,
    durationMs: 20,
    status: "up",
    periodStart: "2026-01-01T00:01:00Z",
    periodType: "raw",
    previousUid: resultUids.oldest,
    nextUid: resultUids.newest,
  },
  [resultUids.newest]: {
    uid: resultUids.newest,
    durationMs: 30,
    status: "up",
    periodStart: "2026-01-01T00:02:00Z",
    periodType: "raw",
    previousUid: resultUids.middle,
  },
};

test.describe("Result detail prev/next navigation", () => {
  async function gotoCheckDetail(page: Parameters<Parameters<typeof test.extend>[0]["authenticatedPage"]>[0]) {
    // The check-detail header's SLO coverage chip fires an unconditional
    // GET /slos?checkUid=… (spec 2026-08-20-01). Stub it so the networkidle
    // wait below has nothing real left to settle.
    await mockSloCoverage(page);

    await page.route("**/api/v1/orgs/*/results*", (route) => {
      const url = route.request().url();
      if (!url.includes("/results")) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: [
            { ...detailFixtures[resultUids.newest] },
            { ...detailFixtures[resultUids.middle] },
            { ...detailFixtures[resultUids.oldest] },
          ],
          pagination: { total: 3, size: 3 },
        }),
      });
    });

    await page.route("**/api/v1/orgs/*/checks/*/results/*", (route) => {
      const url = new URL(route.request().url());
      const uid = url.pathname.split("/").pop() ?? "";
      const fixture = detailFixtures[uid];

      if (!fixture) return route.continue();

      // The region query param (if present) is echoed back so we can assert
      // it round-trips through prev/next without needing a real backend
      // region-scoped computation — the Go service tests already cover that.
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(fixture),
      });
    });

    await page.route("**/api/v1/orgs/*/incidents*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [], pagination: { total: 0, size: 0 } }),
      })
    );

    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");

    const checkName = `E2E Prev Next ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/prev-next-test");
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const url = new URL(page.url());
    const match = url.pathname.match(/\/checks\/([^/]+)/);
    const checkUid = match?.[1] ?? "";

    return checkUid;
  }

  test("newest result has Next disabled, steps back and forward again", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checkUid = await gotoCheckDetail(page);

    await page.goto(`orgs/test/checks/${checkUid}/results/${resultUids.newest}`);
    await page.waitForLoadState("networkidle");

    const previousButton = page.getByTestId("result-nav-previous");
    const nextButton = page.getByTestId("result-nav-next");

    await expect(previousButton).toBeEnabled();
    await expect(nextButton).toBeDisabled();

    // Step back: newest -> middle. Both buttons must be live.
    await previousButton.click();
    await page.waitForURL(`**/results/${resultUids.middle}`, { timeout: 10000 });
    await expect(previousButton).toBeEnabled();
    await expect(nextButton).toBeEnabled();

    // Step back again: middle -> oldest. Previous must now be disabled.
    await previousButton.click();
    await page.waitForURL(`**/results/${resultUids.oldest}`, { timeout: 10000 });
    await expect(previousButton).toBeDisabled();
    await expect(nextButton).toBeEnabled();

    // Step forward twice, back to the newest result — buttons stay live at
    // every step, ending with Next disabled again at the boundary.
    await nextButton.click();
    await page.waitForURL(`**/results/${resultUids.middle}`, { timeout: 10000 });
    await expect(previousButton).toBeEnabled();
    await expect(nextButton).toBeEnabled();

    await nextButton.click();
    await page.waitForURL(`**/results/${resultUids.newest}`, { timeout: 10000 });
    await expect(previousButton).toBeEnabled();
    await expect(nextButton).toBeDisabled();
  });

  test("region search param survives prev/next navigation", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checkUid = await gotoCheckDetail(page);

    await page.goto(
      `orgs/test/checks/${checkUid}/results/${resultUids.middle}?region=eu`
    );
    await page.waitForLoadState("networkidle");

    const previousButton = page.getByTestId("result-nav-previous");
    const nextButton = page.getByTestId("result-nav-next");
    await expect(previousButton).toBeEnabled();
    await expect(nextButton).toBeEnabled();

    await nextButton.click();
    await page.waitForURL(`**/results/${resultUids.newest}?region=eu`, {
      timeout: 10000,
    });
    expect(page.url()).toContain("region=eu");

    await previousButton.click();
    await page.waitForURL(`**/results/${resultUids.middle}?region=eu`, {
      timeout: 10000,
    });
    expect(page.url()).toContain("region=eu");
  });
});
