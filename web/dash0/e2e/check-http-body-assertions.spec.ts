import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-09-16-12: the HTTP check form gained a Body
// assertions section, so a plain-text endpoint (the ASP.NET Core health
// convention, which returns a bare "Healthy" / "Degraded" / "Unhealthy" word)
// can finally be asserted on with EXACT equality and an ignore-case flag.
//
// The regression this guards is the one spec 2026-09-11-01 named: a config key
// the form does not model is silently dropped or resurrected on save. So every
// test here saves through the real form and reads the stored config back.
// Modeled on check-http-json-assertions.spec.ts.

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

test.describe("HTTP check body assertions", () => {
  test("creating a check with no body assertion writes no key", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Body Assertions None ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/body-assertions-none");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("bodyAssertions");
    expect(created.config).not.toHaveProperty("body_assertions");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("an eq + ignore-case assertion persists and survives an edit-page reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Body Assertions ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/DictionaryApi/health");

    // Advanced is collapsed by default.
    await page.getByTestId("section-advanced-trigger").click();
    await page.getByTestId("body-assertion-add").click();
    await expect(page.getByTestId("body-assertion-editor")).toBeVisible();

    // A body assertion has no path input — its subject is the whole body.
    await expect(page.getByTestId("body-assertion-path")).toHaveCount(0);

    await page.getByTestId("body-assertion-operator").click();
    await page.getByRole("option", { name: "equals", exact: true }).click();
    await page.getByTestId("body-assertion-value").fill("Healthy");
    await page.getByTestId("body-assertion-ignore-case").click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.bodyAssertions).toEqual({
      type: "assertion",
      operator: "eq",
      value: "Healthy",
      ignoreCase: true,
    });
    expect(created.config).not.toHaveProperty("body_assertions");
    expect(created.config.url).toBe("https://acme.com/DictionaryApi/health");

    // Reload the edit page: Advanced auto-opens because the check holds a
    // non-default value (httpOptionsSummary), so the assertion is visible
    // without a click. The ignore-case checkbox must come back checked — a
    // flag that silently resets is the whole class of bug this guards.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("body-assertion-editor")).toBeVisible();
    await expect(page.getByTestId("body-assertion-value")).toHaveValue(
      "Healthy",
    );
    await expect(
      page.getByTestId("body-assertion-ignore-case"),
    ).toHaveAttribute("data-state", "checked");

    // Saving again with no changes must not drop it.
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });
    const resaved = await getCheck(page, token, uid);
    expect(resaved.config.bodyAssertions).toEqual({
      type: "assertion",
      operator: "eq",
      value: "Healthy",
      ignoreCase: true,
    });

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("the operator list offers no operator the server would reject", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("section-advanced-trigger").click();
    await page.getByTestId("body-assertion-add").click();
    await page.getByTestId("body-assertion-operator").click();

    // exists / not_exists and the numeric comparisons cannot fail against a
    // raw body, so checkhttp's ValidateBody rejects them. The UI must never
    // offer them in the first place.
    for (const label of [
      "exists",
      "does not exist",
      "greater than",
      "less than",
    ]) {
      await expect(
        page.getByRole("option", { name: label, exact: true }),
      ).toHaveCount(0);
    }
    for (const label of ["equals", "contains", "matches regex"]) {
      await expect(
        page.getByRole("option", { name: label, exact: true }),
      ).toBeVisible();
    }
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
          name: `E2E Body Assertions Clear ${Date.now()}`,
          slug: `e2e-body-assertions-clear-${Date.now()}`,
          type: "http",
          config: {
            url: "https://acme.com/body-assertions-clear",
            bodyAssertions: {
              type: "assertion",
              operator: "eq",
              value: "Healthy",
              ignoreCase: true,
            },
          },
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid } = await createResp.json();

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("body-assertion-editor")).toBeVisible();

    await page.getByTestId("body-assertion-remove").click();
    await expect(page.getByTestId("body-assertion-editor")).toHaveCount(0);
    await expect(page.getByTestId("body-assertion-add")).toBeVisible();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });

    const updated = await getCheck(page, token, uid);
    expect(updated.config).not.toHaveProperty("bodyAssertions");
    expect(updated.config).not.toHaveProperty("body_assertions");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("a body assertion coexists with a JSONPath assertion on the same check", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: `E2E Both Assertions ${Date.now()}`,
          slug: `e2e-both-assertions-${Date.now()}`,
          type: "http",
          config: {
            url: "https://acme.com/both-assertions",
            bodyAssertions: {
              type: "assertion",
              operator: "not_contains",
              value: "error",
            },
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

    // Two editors on one form: the testIdPrefix split is what keeps them from
    // colliding, and a save must preserve both trees.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("json-assertion-editor")).toBeVisible();
    await expect(page.getByTestId("body-assertion-editor")).toBeVisible();
    await expect(page.getByTestId("json-assertion-path")).toHaveValue(
      "$.status",
    );
    await expect(page.getByTestId("body-assertion-value")).toHaveValue("error");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });

    const resaved = await getCheck(page, token, uid);
    expect(resaved.config.bodyAssertions).toEqual({
      type: "assertion",
      operator: "not_contains",
      value: "error",
    });
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
});
