import { test, expect, API_BASE, type Page } from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

// Coverage for spec 2026-10-03-03 (multi-step checks, website crawl): the
// form creates a crawl check with its defaults left implicit, the check runs
// as resumable slices (a worker task since spec 2026-10-22-03) and finishes
// with one result, and the check page shows the crawl card with the run's
// report. The run endpoints answer and cancel. Spec 2026-10-22-03: the region
// picker lists private locations, and disables one whose agents cannot crawl.

// Mocked so every crawl capability state is present deterministically.
const REGIONS = [
  { slug: "eu-west", emoji: "🇫🇷", name: "EU West", capabilities: { crawl: "yes" } },
  { slug: "@dc1", emoji: "🏢", name: "DC1", private: true, capabilities: { crawl: "yes" } },
  { slug: "@old-dc", emoji: "🏚️", name: "Old DC", private: true, capabilities: { crawl: "no" } },
  { slug: "@new-dc", emoji: "🏗️", name: "New DC", private: true },
];

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Website crawl check", () => {
  test("creates a crawl check, runs it and shows its report", async ({ authenticatedPage }) => {
    test.setTimeout(120_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };

    await page.goto("orgs/test/checks/new?checkType=crawl");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Robots and links default on.
    await expect(page.getByTestId("check-crawl-respectRobots-checkbox")).toHaveAttribute(
      "data-state",
      "checked",
    );
    await expect(page.getByTestId("check-crawl-checkExternalLinks-checkbox")).toHaveAttribute(
      "data-state",
      "checked",
    );

    // The URL is required.
    await page.getByTestId("check-name-input").fill(`E2E crawl ${Date.now()}`);
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByText("URL is required").first()).toBeVisible();

    // Crawl this very server's dashboard shell, three pages at most, no
    // external links (the CI runner may have no outbound network).
    await page.getByTestId("check-crawl-url-input").fill(`${API_BASE}/d/`);
    await page.getByTestId("check-crawl-max-pages-input").fill("3");
    await page.getByTestId("check-crawl-checkExternalLinks-checkbox").click();
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers })
    ).json();
    expect(created.type).toBe("crawl");
    expect(created.config.maxPages).toBe(3);
    expect(created.config.checkExternalLinks).toBe(false);
    expect(created.config, "defaults stay implicit").not.toHaveProperty("respectRobots");
    expect(created.regions?.length ?? 0).toBeLessThanOrEqual(1);

    // The crawl card is on the check page.
    await expect(page.getByTestId("crawl-card")).toBeVisible();

    // The run finishes (worker-task slices, claimed by the cloud worker's
    // task loop) with one stored report.
    await expect
      .poll(
        async () => {
          const resp = await page.request.get(
            `${API_BASE}/api/v1/orgs/test/checks/${uid}/crawl-reports`,
            { headers },
          );
          return resp.ok() ? (await resp.json()).data.length : -1;
        },
        { timeout: 90_000, intervals: [2_000] },
      )
      .toBeGreaterThanOrEqual(1);

    const run = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}/run`, { headers })
    ).json();
    expect(run.running).toBe(false);

    await page.reload();
    await expect(page.getByTestId("crawl-reports")).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId("crawl-card")).toContainText(/pages? crawled/);

    // Cancel is a no-op without a run in progress, and refused on other types.
    const cancel = await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}/run`, {
      headers,
    });
    expect(cancel.status()).toBe(204);
  });

  test("refuses a crawl run every 30 minutes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { type: "crawl", period: "00:30:00", config: { url: "https://www.acme.com/" } },
    });
    expect(resp.status()).toBe(400);
  });

  test("lists private regions, and disables one whose agents cannot crawl", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.route("**/api/v1/orgs/*/regions", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: REGIONS, defaultRegions: ["eu-west"] }),
      }),
    );

    await page.goto("orgs/test/checks/new?checkType=crawl");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    // Every region is listed, private locations included.
    expect(await page.locator("[data-testid^='region-option-']").count()).toBe(4);

    // A private location whose agents crawl is selectable (positive control).
    const dc1 = page.getByTestId("region-checkbox-@dc1");
    await expect(dc1).toBeEnabled();
    await dc1.click();
    await expect(dc1).toHaveAttribute("data-state", "checked");

    // One whose agents report no crawl capability is disabled, with a hint.
    await expect(page.getByTestId("region-option-@old-dc")).toHaveAttribute("data-crawl", "no");
    await expect(page.getByTestId("region-checkbox-@old-dc")).toBeDisabled();
    await expect(page.getByTestId("region-crawl-unsupported-@old-dc")).toBeVisible();

    // A location with no live agent yet stays selectable.
    await expect(page.getByTestId("region-checkbox-@new-dc")).toBeEnabled();
    await expect(page.getByTestId("region-crawl-unsupported-@new-dc")).toHaveCount(0);
  });
});
