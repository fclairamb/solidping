import {
  API_BASE,
  createHeartbeatCheck,
  expect,
  getAuthToken,
  mockSloCoverage,
  test,
  type Page,
} from "./fixtures";

// The "No data" (stale) status, spec 2026-09-25-02.
//
// A check only goes stale after max(3 × period, 5 min) of silence, measured by a
// minute sweep — far too slow for an end-to-end run. So the API surface is
// exercised for real (the ?status=stale filter token and the byStatus key, both
// of which answered 400 / were absent before), and the rendering is exercised by
// letting the real check-detail response through and rewriting only the fields
// the freshness sweep would have written. Everything else on the page — the
// check itself, its results, the layout — is the live server's.

const EIGHT_HOURS_AGO = () => new Date(Date.now() - 8 * 3600_000).toISOString();
const JUST_NOW = () => new Date(Date.now() - 20_000).toISOString();

/** Rewrites the GET of one check's detail with `patch` applied on top of the
 * server's real answer. Matches the pathname exactly, so sub-resources
 * (/results, /availability, …) pass through untouched. */
async function patchCheckDetail(
  page: Page,
  uid: string,
  patch: Record<string, unknown>,
): Promise<void> {
  await page.route(
    (url) => url.pathname === `/api/v1/orgs/test/checks/${uid}`,
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const response = await route.fetch();
      const body = (await response.json()) as Record<string, unknown>;
      await route.fulfill({ response, json: { ...body, ...patch } });
    },
  );
}

test.describe("No data (stale) status", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    await mockSloCoverage(authenticatedPage);
  });

  test("the API accepts ?status=stale and always reports a stale byStatus key", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };

    const list = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks?status=stale`, {
      headers,
    });
    expect(list.status()).toBe(200);
    const listed = (await list.json()) as { data: Array<{ status?: string }> };
    expect(Array.isArray(listed.data)).toBe(true);
    for (const check of listed.data) {
      expect(check.status).toBe("stale");
    }

    const stats = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/stats`, {
      headers,
    });
    expect(stats.status()).toBe(200);
    const body = (await stats.json()) as { byStatus: Record<string, number> };
    expect(body.byStatus).toHaveProperty("stale");
  });

  test("a stale check reads 'No data since …', never up, on its detail page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Stale ${Date.now()}`,
      undefined,
      "01:00:00",
    );

    const lastSeen = EIGHT_HOURS_AGO();
    await patchCheckDetail(page, check.uid, {
      status: "stale",
      statusChangedAt: lastSeen,
      lastResultAt: lastSeen,
      regionFreshness: [{ region: "", lastResultAt: lastSeen, stale: true }],
    });

    await page.goto(`orgs/test/checks/${check.uid}`);
    const header = page.getByTestId("check-detail-header");
    await expect(header).toBeVisible();

    // The gray clock badge, labelled — never the raw wire token.
    const badge = header.locator('[data-status="stale"]').first();
    await expect(badge).toBeVisible();
    await expect(badge).toHaveText("No data");

    await expect(page.getByTestId("check-stale-since")).toContainText("No data since");
    await expect(page.getByTestId("region-freshness")).toBeVisible();

    // The summary card times the status from status_changed_at.
    await expect(page.getByText("No data for", { exact: true })).toBeVisible();
    await expect(page.getByTestId("summary-status-duration")).toContainText("h");
  });

  test("one silent region is named while the others keep the check up", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Silent Region ${Date.now()}`,
      undefined,
      "01:00:00",
    );

    await patchCheckDetail(page, check.uid, {
      status: "up",
      lastResultAt: JUST_NOW(),
      regionFreshness: [
        { region: "eu-west", lastResultAt: JUST_NOW(), stale: false },
        { region: "lauterbourg", lastResultAt: EIGHT_HOURS_AGO(), stale: true },
        { region: "us-east", lastResultAt: JUST_NOW(), stale: false },
      ],
    });

    await page.goto(`orgs/test/checks/${check.uid}`);
    await expect(page.getByTestId("check-detail-header")).toBeVisible();

    // Still being checked: the status is not stale.
    await expect(page.getByTestId("check-stale-since")).toHaveCount(0);

    const silent = page.getByTestId("region-freshness-silent");
    await expect(silent).toHaveCount(1);
    await expect(silent).toContainText("lauterbourg");
    await expect(silent).toContainText("2 other regions reporting");

    await expect(page.locator('[data-testid="region-freshness-row"][data-stale="true"]')).toHaveCount(1);

    // "Last checked" lists each region's own age when they disagree, so one
    // live region cannot hide a dead one.
    await expect(page.getByTestId("summary-region-ages")).toContainText("lauterbourg");
  });
});
