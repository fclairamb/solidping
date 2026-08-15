import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-15-07: per-type docs anchor, domain
// warning/critical-days thresholds, and long (>24h) check periods.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("Domain check — docs anchor, thresholds, long period", () => {
  test("docs link points at the domain-expiration anchor", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=domain");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-domain-input")).toBeVisible();

    const docsLink = page.getByTestId("docs-link");
    await expect(docsLink).toHaveAttribute(
      "href",
      "/docs/features/check-types#domain-expiration",
    );
  });

  test("setting warning/critical days and a 2-week period round-trips through save and reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=domain");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-domain-input")).toBeVisible();

    const checkName = `E2E Domain Thresholds ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-domain-input")
      .fill("example-domain-thresholds.test");

    await page.getByTestId("check-domain-critical-days-input").fill("7");
    await page.getByTestId("check-domain-warning-days-input").fill("30");

    // Long-horizon period: domain has no MaxPeriod, so "2 weeks" must be
    // offered in the interval select.
    await page.getByTestId("check-period-select").click();
    await page.getByRole("option", { name: "2 weeks", exact: true }).click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.criticalDays).toBe(7);
    expect(created.config.warningDays).toBe(30);
    expect(created.period).toBe("336:00:00");

    // Reload the edit page — values must reflect what was actually persisted,
    // not just what the client held after submit.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId("check-domain-critical-days-input"),
    ).toHaveValue("7");
    await expect(
      page.getByTestId("check-domain-warning-days-input"),
    ).toHaveValue("30");
    await expect(page.getByTestId("check-period-select")).toContainText(
      "2 weeks",
    );

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
