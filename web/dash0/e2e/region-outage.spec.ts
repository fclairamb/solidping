import {
  API_BASE,
  createHeartbeatCheck,
  expect,
  getAuthToken,
  mockSloCoverage,
  test,
  type Page,
} from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

// Region outage banners, spec 2026-09-25-03.
//
// A region only reads offline after its last worker has been silent for 5
// minutes and the minute region sweep has run — nothing an end-to-end run can
// arrange against the shared dev server without killing its workers. So the
// API field is checked for real (every cloud region carries a `status`), and
// the rendering is exercised by rewriting only what the sweep would have
// produced: the regions list marks one region offline, and the check under
// test is pointed at it. The check itself, the pages and the layout are the
// live server's.

const OFFLINE_SINCE = () => new Date(Date.now() - 2 * 3600_000).toISOString();

const REGIONS = () => [
  { slug: "e2e-up", emoji: "🇫🇷", name: "E2E Up", status: "online" },
  {
    slug: "e2e-down",
    emoji: "🇫🇷",
    name: "E2E Down",
    status: "offline",
    offlineSince: OFFLINE_SINCE(),
  },
];

async function mockRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS(), defaultRegions: ["e2e-up"] }),
    }),
  );
}

/** Points one check at `regions`, on its detail GET and in the checks list. */
async function pinCheckTo(page: Page, uid: string, regions: string[]): Promise<void> {
  await page.route(
    (url) =>
      url.pathname === `/api/v1/orgs/test/checks/${uid}` ||
      url.pathname === "/api/v1/orgs/test/checks",
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const response = await route.fetch();
      const body = (await response.json()) as Record<string, unknown>;
      if (Array.isArray(body.data)) {
        const data = (body.data as Array<Record<string, unknown>>).map((check) =>
          check.uid === uid ? { ...check, regions } : check,
        );
        return route.fulfill({ response, json: { ...body, data } });
      }
      return route.fulfill({ response, json: { ...body, regions } });
    },
  );
}

test.describe("region outage", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    await mockSloCoverage(authenticatedPage);
  });

  test("the org regions endpoint reports a status on every cloud region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const response = await page.request.get(`${API_BASE}/api/v1/orgs/test/regions`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(response.status()).toBe(200);

    const body = (await response.json()) as {
      data: Array<{ slug: string; private?: boolean; status?: string }>;
    };
    const cloud = body.data.filter((region) => !region.private);
    expect(cloud.length).toBeGreaterThan(0);
    for (const region of cloud) {
      expect(["online", "offline"]).toContain(region.status);
    }
  });

  test("a check pinned only to an offline region says it is not running", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(page, token, `E2E Blind ${Date.now()}`, undefined, "01:00:00");

    await mockRegions(page);
    await pinCheckTo(page, check.uid, ["e2e-down"]);

    await page.goto(`orgs/test/checks/${check.uid}`);
    const banner = page.getByTestId("region-outage-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toHaveAttribute("data-kind", "blind");
    await expect(banner).toContainText("E2E Down");
    await expect(banner).toContainText("this check is not running");
  });

  test("a check with another live region says it keeps running", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(page, token, `E2E Reduced ${Date.now()}`, undefined, "01:00:00");

    await mockRegions(page);
    await pinCheckTo(page, check.uid, ["e2e-down", "e2e-up"]);

    await page.goto(`orgs/test/checks/${check.uid}`);
    const banner = page.getByTestId("region-outage-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toHaveAttribute("data-kind", "reduced");
    await expect(banner).toContainText("running from 1 of 2 regions");
  });

  test("the checks list names the checks that stopped", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const name = `E2E Outage List ${Date.now()}`;
    const check = await createHeartbeatCheck(page, token, name, undefined, "01:00:00");

    await mockRegions(page);
    await pinCheckTo(page, check.uid, ["e2e-down"]);

    await page.goto("orgs/test/checks");
    const banner = page.getByTestId("checks-region-outage-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("E2E Down");
    await expect(page.getByTestId("checks-region-outage-blind")).toContainText(name);
  });

  test("no banner while every region is online", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const check = await createHeartbeatCheck(page, token, `E2E Online ${Date.now()}`, undefined, "01:00:00");

    await mockRegions(page);
    await pinCheckTo(page, check.uid, ["e2e-up"]);

    await page.goto(`orgs/test/checks/${check.uid}`);
    await expect(page.getByTestId("check-detail-header")).toBeVisible();
    await expect(page.getByTestId("region-outage-banner")).toHaveCount(0);
  });

  test("the check form warns before pinning a check to an offline region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    await expect(page.getByTestId("region-offline-e2e-down")).toBeVisible();
    await expect(page.getByTestId("check-regions-offline-warning")).toHaveCount(0);

    await page.getByTestId("region-option-e2e-down").getByRole("checkbox").click();
    await expect(page.getByTestId("check-regions-offline-warning")).toContainText("E2E Down");
  });
});
