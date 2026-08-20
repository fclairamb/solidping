import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-20-01: the HTTP check form's Advanced section
// gained a "Capture the failing response" switch (capture_failure_response).
// Unlike verifySsl / followRedirects it defaults OFF — response bodies can
// carry PII, so the org opts in per check. Modeled on
// check-http-verify-ssl-follow-redirects.spec.ts.

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

test.describe("HTTP check capture_failure_response", () => {
  test("defaults off, writes no key, and shows no privacy notice", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Advanced is a collapsed-by-default section — open it.
    await page.getByTestId("section-advanced-trigger").click();
    await expect(
      page.getByTestId("check-capture-failure-response-switch"),
    ).toBeVisible();
    await expect(
      page.getByTestId("check-capture-failure-response-switch"),
    ).toHaveAttribute("aria-checked", "false");
    await expect(
      page.getByTestId("check-capture-failure-response-warning"),
    ).toHaveCount(0);

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Capture Default ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/capture-default");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("capture_failure_response");
    expect(created.config).not.toHaveProperty("captureFailureResponse");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("turning it on shows the privacy notice and persists across an edit reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("section-advanced-trigger").click();

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Capture On ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://acme.com/capture-on");

    await page.getByTestId("check-capture-failure-response-switch").click();
    await expect(
      page.getByTestId("check-capture-failure-response-warning"),
    ).toBeVisible();
    await expect(
      page.getByTestId("check-capture-failure-response-warning"),
    ).toContainText("never appears on a status page");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    // Canonical snake_case key only — never the camelCase alias.
    expect(created.config.capture_failure_response).toBe(true);
    expect(created.config).not.toHaveProperty("captureFailureResponse");
    // And the other Advanced toggles are untouched by it.
    expect(created.config).not.toHaveProperty("verifySsl");
    expect(created.config).not.toHaveProperty("followRedirects");

    // Reload the edit page: Advanced auto-opens (it holds a non-default
    // value), so the switch and its notice are visible without a click.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId("check-capture-failure-response-switch"),
    ).toHaveAttribute("aria-checked", "true");
    await expect(
      page.getByTestId("check-capture-failure-response-warning"),
    ).toBeVisible();

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
