import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-10-17-02 (vendor status page check): the form creates
// a check from the catalog with one component and the edit form reopens it with
// the component still ticked. The vendor page is a fixture: the components
// lookup (the only call that would reach the vendor from the browser) is
// answered by the test, so nothing leaves the machine.

const COMPONENTS = {
  data: [
    { id: "br0l2tvcx85d", name: "Actions", group: "Core" },
    { id: "kr09ddfgbfsf", name: "Issues", group: "Core" },
  ],
};

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Vendor status page check", () => {
  test("creates a check from the catalog with one component and reopens it", async ({
    authenticatedPage,
  }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;
    await page.route("**/checks/vendor-status/components", (route) =>
      route.fulfill({ json: COMPONENTS }),
    );

    await page.goto("orgs/test/checks/new?checkType=vendor-status");
    await expect(page.getByTestId("check-vendor-status-catalog")).toBeVisible();

    // The catalog is searchable, and a custom URL is offered.
    await page.getByTestId("check-vendor-status-catalog-search").fill("github");
    await expect(page.getByTestId("check-vendor-status-catalog-github")).toBeVisible();
    await expect(page.getByTestId("check-vendor-status-custom")).toBeVisible();
    await page.getByTestId("check-vendor-status-custom").click();
    await expect(page.getByTestId("check-vendor-status-url-input")).toBeVisible();
    await page.getByTestId("check-vendor-status-catalog-github").click();

    await page.getByTestId("check-vendor-status-component-br0l2tvcx85d").click();
    await page.getByTestId("check-name-input").fill(`E2E vendor ${Date.now()}`);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const token = await getAuthToken(page);
    const created = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
    ).json();
    expect(created.type).toBe("vendor-status");
    expect(created.config.url).toBe("https://www.githubstatus.com");
    expect(created.config.provider).toBe("github");
    expect(created.config.components).toEqual(["br0l2tvcx85d"]);

    // The check page shows the vendor card, then the edit form keeps the choice.
    await page.goto(`orgs/test/checks/${uid}`);
    await expect(page.getByTestId("vendor-status-card")).toBeVisible();

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("check-vendor-status-component-br0l2tvcx85d")).toBeChecked();
    await expect(page.getByTestId("check-vendor-status-component-kr09ddfgbfsf")).not.toBeChecked();
  });

  test("a custom url must be https", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/checks/new?checkType=vendor-status");
    await page.getByTestId("check-vendor-status-custom").click();
    await page.getByTestId("check-vendor-status-url-input").fill("http://status.acme.com");
    await page.getByTestId("check-name-input").fill("E2E vendor invalid");
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("check-vendor-status-url-error")).toBeVisible();
  });
});
