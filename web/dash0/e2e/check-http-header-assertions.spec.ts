import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-10-12-11: the HTTP check form gained a Header assertions section
// (not_exists / not_contains on a response header). Saves through the real
// form and reads the stored config back. Modeled on
// check-http-body-assertions.spec.ts.

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

test.describe("HTTP check header assertions", () => {
  test("a not_exists leaf on X-Powered-By persists and reloads", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await page
      .getByTestId("check-name-input")
      .fill(`E2E Header Assertions ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/header-assertions");

    await page.getByTestId("section-advanced-trigger").click();
    await page.getByTestId("header-assertion-add").click();
    await expect(page.getByTestId("header-assertion-editor")).toBeVisible();

    await page.getByTestId("header-assertion-path").fill("X-Powered-By");
    await page.getByTestId("header-assertion-operator").click();
    await page
      .getByRole("option", { name: "does not exist", exact: true })
      .click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.headerAssertions).toMatchObject({
      type: "assertion",
      path: "X-Powered-By",
      operator: "not_exists",
    });
    expect(created.config).not.toHaveProperty("header_assertions");

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("header-assertion-editor")).toBeVisible();
    await expect(page.getByTestId("header-assertion-path")).toHaveValue(
      "X-Powered-By",
    );

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });
    const resaved = await getCheck(page, token, uid);
    expect(resaved.config.headerAssertions).toMatchObject({
      type: "assertion",
      path: "X-Powered-By",
      operator: "not_exists",
    });

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("clearing the assertion removes it, and headers_pattern survives", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: `E2E Header Assertions Clear ${Date.now()}`,
          slug: `e2e-header-assertions-clear-${Date.now()}`,
          type: "http",
          config: {
            url: "https://acme.com/header-assertions-clear",
            headersPattern: { "Content-Type": "text" },
            headerAssertions: {
              type: "assertion",
              path: "Cache-Control",
              operator: "not_contains",
              value: "no-store",
            },
          },
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid } = await createResp.json();

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("header-assertion-editor")).toBeVisible();
    await page.getByTestId("header-assertion-remove").click();
    await expect(page.getByTestId("header-assertion-editor")).toHaveCount(0);

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });

    const updated = await getCheck(page, token, uid);
    expect(updated.config).not.toHaveProperty("headerAssertions");
    expect(updated.config).not.toHaveProperty("header_assertions");
    expect(updated.config.headersPattern).toEqual({ "Content-Type": "text" });

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
