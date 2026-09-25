import { API_BASE, expect, getAuthToken, test, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Multi-region quorum, spec 2026-09-25-10.
//
// The test server has a single cloud region ("default"): the form's quorum
// field only renders from two regions, and a real regional issue needs
// readings from several regions, which only workers produce. So the regions
// list is mocked with three cloud regions, and the check detail response is
// the live server's with the quorum fields patched in. Which results make a
// regional issue is the backend's job and is covered by the Go tests
// (internal/handlers/incidents/quorum_test.go); this pins the UI contract.

const REGIONS = [
  { slug: "default", emoji: "🇪🇺", name: "Default", status: "online" },
  { slug: "e2e-second", emoji: "🇫🇷", name: "E2E Second", status: "online" },
  { slug: "e2e-third", emoji: "🇯🇵", name: "E2E Third", status: "online" },
];

async function mockThreeRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS, defaultRegions: ["default"] }),
    }),
  );
}

async function createCheck(page: Page, token: string, name: string): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, type: "http", config: { url: "https://example.com/quorum" } },
  });
  expect(resp.ok()).toBeTruthy();

  return ((await resp.json()) as { uid: string }).uid;
}

async function deleteCheck(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

/**
 * Serves the check detail from the live server with `patch` applied: only
 * GET /checks/<uid> itself (with or without a query string), never its
 * sub-resources.
 */
async function patchCheckDetail(
  page: Page,
  uid: string,
  patch: Record<string, unknown>,
): Promise<void> {
  await page.route(
    (url) => url.pathname.endsWith(`/api/v1/orgs/test/checks/${uid}`),
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }

      const response = await route.fetch();
      const body = (await response.json()) as Record<string, unknown>;
      await route.fulfill({ response, json: { ...body, ...patch } });
    },
  );
}

test.describe("Multi-region quorum", () => {
  test("the form offers 'Regions that must fail' from two regions and sends it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await mockThreeRegions(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    // Automatic placement on 2 of the 3 mocked regions: the field shows.
    await expect(page.getByTestId("check-placement-auto-summary")).toContainText("Automatic (2 regions)");

    await expandSection(page, "section-incident-tracking-trigger");
    const section = page.getByTestId("check-fail-quorum-section");
    await expect(section).toBeVisible();

    // Pre-filled with the default, which for 2 regions is all of them.
    await expect(page.getByTestId("check-fail-quorum-select")).toContainText("Default");
    await expect(page.getByTestId("check-fail-quorum-resolved")).toContainText("Down when all 2 regions fail");

    // An explicit count: out of range is refused before any request.
    await page.getByTestId("check-fail-quorum-select").click();
    await page.getByRole("option", { name: "A number of regions", exact: true }).click();
    await page.getByTestId("check-fail-quorum-count").fill("0");
    await expect(page.getByTestId("check-fail-quorum-error")).toBeVisible();

    await page.getByTestId("check-fail-quorum-count").fill("1");
    await expect(page.getByTestId("check-fail-quorum-error")).toHaveCount(0);
    await expect(page.getByTestId("check-fail-quorum-resolved")).toContainText("Down when 1 of 2 regions fail");

    const name = `E2E Fail Quorum ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(name);
    await page.getByTestId("check-url-input").fill("https://example.com/fail-quorum");

    const [request] = await Promise.all([
      page.waitForRequest(
        (req) => req.url().includes("/api/v1/orgs/test/checks") && req.method() === "POST",
      ),
      page.getByTestId("check-submit-button").click(),
    ]);
    expect((request.postDataJSON() as Record<string, unknown>).failQuorum).toBe(1);

    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().split("/checks/")[1].split(/[/?#]/)[0];

    const stored = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(((await stored.json()) as Record<string, unknown>).failQuorum).toBe(1);

    await deleteCheck(page, token, uid);
  });

  test("a regional issue is named on the check page, distinct from down and no data", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await mockThreeRegions(page);

    const uid = await createCheck(page, token, `E2E Regional Issue ${Date.now()}`);
    const now = Date.now();
    await patchCheckDetail(page, uid, {
      status: "warning",
      placement: "pinned",
      regions: ["default", "e2e-second", "e2e-third"],
      failQuorum: "default",
      effectiveFailQuorum: 2,
      regionalIssue: { failingRegions: ["e2e-third"], failQuorum: 2, regionCount: 3 },
      regionFreshness: [
        { region: "default", lastResultAt: new Date(now - 20_000).toISOString(), stale: false, status: "up" },
        { region: "e2e-second", lastResultAt: new Date(now - 30_000).toISOString(), stale: false, status: "up" },
        {
          region: "e2e-third",
          lastResultAt: new Date(now - 10_000).toISOString(),
          stale: false,
          status: "timeout",
          statusSince: new Date(now - 14 * 60_000).toISOString(),
        },
      ],
    });

    await page.goto(`orgs/test/checks/${uid}`);

    const banner = page.getByTestId("regional-issue-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("Regional issue");
    await expect(banner).toContainText("E2E Third");
    await expect(banner).toContainText("1 of 3 regions failing");
    await expect(banner).toContainText("2 regions fail");

    // Not an outage: neither the region-offline banner nor an incident.
    await expect(page.getByTestId("region-outage-banner")).toHaveCount(0);

    // The placement block states the rule and marks the failing region.
    await expect(page.getByTestId("check-placement-quorum")).toContainText("2 of 3 regions");
    const failing = page.locator("[data-testid='check-placement-region'][data-region='e2e-third']");
    await expect(failing).toHaveAttribute("data-failing", "true");
    await expect(failing).toContainText("failing since");
    await expect(
      page.locator("[data-testid='check-placement-region'][data-region='default']"),
    ).not.toHaveAttribute("data-failing", "true");

    await deleteCheck(page, token, uid);
  });

  test("no banner without a regional issue", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createCheck(page, token, `E2E No Regional Issue ${Date.now()}`);

    await page.goto(`orgs/test/checks/${uid}`);
    await expect(page.getByTestId("check-placement")).toBeVisible();
    await expect(page.getByTestId("regional-issue-banner")).toHaveCount(0);

    await deleteCheck(page, token, uid);
  });
});
