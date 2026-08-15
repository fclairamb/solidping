import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-15-05: the domain check form's Advanced section
// gained a "Lookup method" select (auto/rdap/whois) driving the backend's
// RDAP-first-with-WHOIS-fallback dispatch. Modeled on
// check-http-verify-ssl-follow-redirects.spec.ts's create-through-the-real-
// form, then-reload-the-edit-page pattern.

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

test.describe("Domain check lookup method", () => {
  test("defaults to Auto and writes no method key when untouched", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=domain");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-domain-input")).toBeVisible();

    // Advanced is a collapsed-by-default section — open it.
    await page.getByTestId("section-advanced-trigger").click();
    await expect(page.getByTestId("check-domain-method-select")).toBeVisible();
    await expect(page.getByTestId("check-domain-method-select")).toContainText(
      "Auto",
    );

    const checkName = `E2E Domain Method Default ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-domain-input")
      .fill("example-domain-method-default.test");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("method");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("selecting WHOIS only saves method: whois and round-trips on reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=domain");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-domain-input")).toBeVisible();

    await page.getByTestId("section-advanced-trigger").click();

    const checkName = `E2E Domain Method Whois ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-domain-input")
      .fill("example-domain-method-whois.test");

    await page.getByTestId("check-domain-method-select").click();
    await page.getByRole("option", { name: "WHOIS only", exact: true }).click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.method).toBe("whois");

    // Reload the edit page — the Advanced section auto-opens (non-default
    // value) and the select reflects the saved method.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-domain-method-select")).toContainText(
      "WHOIS only",
    );

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
