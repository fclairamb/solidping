import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-28-12: the HTTP check form's Advanced section
// never showed or preserved jsonPathAssertions — JsonAssertionEditor existed
// but was imported nowhere, and toConfig rebuilt the config from scratch, so
// saving an HTTP check with assertions from the UI silently destroyed them.
// This is the regression the spec's bug report describes: create/edit a
// JSONPath assertion through the UI, save, reload the edit page, and confirm
// it is still shown. Modeled on check-http-capture-failure-response.spec.ts.

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

test.describe("HTTP check JSONPath assertions", () => {
  test("creating a check with no assertion writes no key", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page
      .getByTestId("check-name-input")
      .fill(`E2E JSON Assertions None ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/json-assertions-none");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("jsonPathAssertions");
    expect(created.config).not.toHaveProperty("json_path_assertions");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("adding an assertion persists it and survives an edit-page reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page
      .getByTestId("check-name-input")
      .fill(`E2E JSON Assertions ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/json-assertions");

    // Advanced is a collapsed-by-default section — open it, then add an
    // assertion via the editor's initial add button.
    await page.getByTestId("section-advanced-trigger").click();
    await page.getByTestId("json-assertion-add").click();
    await expect(page.getByTestId("json-assertion-editor")).toBeVisible();

    await page.getByTestId("json-assertion-path").fill("$.status");
    await page
      .getByTestId("json-assertion-operator")
      .click();
    await page.getByRole("option", { name: "eq", exact: true }).click();
    await page.getByTestId("json-assertion-value").fill("ok");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.jsonPathAssertions).toEqual({
      type: "assertion",
      path: "$.status",
      operator: "eq",
      value: "ok",
    });
    expect(created.config).not.toHaveProperty("json_path_assertions");
    // Saving the assertion must not have clobbered the URL/other fields.
    expect(created.config.url).toBe("https://acme.com/json-assertions");

    // Reload the edit page: Advanced auto-opens (it holds a non-default
    // value — see httpOptionsSummary), so the assertion is visible without a
    // click. This is the exact regression the spec fixes: before the wiring,
    // fromConfig never read jsonPathAssertions, so the editor stayed empty
    // here and the next save would have silently dropped it.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("json-assertion-editor")).toBeVisible();
    await expect(page.getByTestId("json-assertion-path")).toHaveValue(
      "$.status",
    );
    await expect(page.getByTestId("json-assertion-value")).toHaveValue("ok");

    // Saving again with no changes must not drop the assertion (the
    // fromConfig -> toConfig round trip the spec's unit tests also cover).
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });
    const resaved = await getCheck(page, token, uid);
    expect(resaved.config.jsonPathAssertions).toEqual({
      type: "assertion",
      path: "$.status",
      operator: "eq",
      value: "ok",
    });

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("clearing the assertion via the edit page removes it on save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: `E2E JSON Assertions Clear ${Date.now()}`,
          slug: `e2e-json-assertions-clear-${Date.now()}`,
          type: "http",
          config: {
            url: "https://acme.com/json-assertions-clear",
            jsonPathAssertions: {
              type: "assertion",
              path: "$.status",
              operator: "eq",
              value: "ok",
            },
          },
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid } = await createResp.json();

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("json-assertion-editor")).toBeVisible();

    await page.getByTestId("json-assertion-remove").click();
    await expect(page.getByTestId("json-assertion-editor")).toHaveCount(0);
    await expect(page.getByTestId("json-assertion-add")).toBeVisible();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });

    const updated = await getCheck(page, token, uid);
    expect(updated.config).not.toHaveProperty("jsonPathAssertions");
    expect(updated.config).not.toHaveProperty("json_path_assertions");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
