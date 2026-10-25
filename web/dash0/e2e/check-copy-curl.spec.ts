import { test, expect, API_BASE, mockSloCoverage } from "./fixtures";

test.beforeEach(async ({ authenticatedPage }) => {
  await mockSloCoverage(authenticatedPage);
});

async function createCheck(
  page: import("@playwright/test").Page,
  data: Record<string, unknown>,
) {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const token = (await resp.json()).accessToken;
  const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { period: "00:05:00", ...data },
  });
  expect(created.status()).toBe(201);
  return created.json();
}

test.describe("Copy check as cURL", () => {
  test("copies the request without leaking secrets", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    const check = await createCheck(page, {
      type: "http",
      name: `E2E Curl ${Date.now()}`,
      config: {
        url: "https://example.com/curl",
        method: "POST",
        body: '{"ping":"pong"}',
        headers: { "X-Plain": "visible-value" },
        secretHeaders: { "X-Api-Key": "super-secret-header-value" },
        basicAuth: "bob:super-secret-password",
      },
    });

    await page.goto(`orgs/test/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("check-more-actions").click();
    await page.getByTestId("check-copy-curl").click();

    await expect(page.locator("[data-sonner-toaster]")).toContainText(/Request copied as cURL/);
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    expect(copied).toContain("'https://example.com/curl'");
    expect(copied).toContain("'X-Plain: visible-value'");
    expect(copied).toContain(`--data-raw '{"ping":"pong"}'`);
    expect(copied).toContain("-X POST");
    expect(copied).toContain('-u "$SOLIDPING_BASIC_AUTH"');
    expect(copied).not.toContain("super-secret-header-value");
    expect(copied).not.toContain("super-secret-password");
    expect(copied).not.toContain("X-Api-Key");
  });

  test("a TCP check has no copy-as-cURL item", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const check = await createCheck(page, {
      type: "tcp",
      name: `E2E Curl TCP ${Date.now()}`,
      config: { host: "example.com", port: 443 },
    });

    await page.goto(`orgs/test/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("check-more-actions").click();
    await expect(page.getByTestId("check-history-link")).toBeVisible();
    await expect(page.getByTestId("check-copy-curl")).toHaveCount(0);
  });
});
