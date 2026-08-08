import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-08-01: the HTTP check form's Advanced section
// gained two switches — "Verify TLS certificate" (verifySsl) and "Follow
// redirects" (followRedirects) — both default on, matching today's hardcoded
// checker behavior. Modeled on check-http-expected-status-codes.spec.ts's
// create-through-the-real-form, then-reload-the-edit-page pattern.

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

test.describe("HTTP check verifySsl / followRedirects", () => {
  test("both switches default on and neither key is written when untouched", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Advanced is a collapsed-by-default section — open it.
    await page.getByTestId("section-advanced-trigger").click();
    await expect(page.getByTestId("check-verify-ssl-switch")).toBeVisible();
    await expect(page.getByTestId("check-follow-redirects-switch")).toBeVisible();

    // Both default on, no warning hint shown.
    await expect(page.getByTestId("check-verify-ssl-switch")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(
      page.getByTestId("check-follow-redirects-switch"),
    ).toHaveAttribute("aria-checked", "true");
    await expect(page.getByTestId("check-verify-ssl-warning")).toHaveCount(0);

    const checkName = `E2E VerifySsl Default ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/verify-ssl-default");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("verifySsl");
    expect(created.config).not.toHaveProperty("followRedirects");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("turning verifySsl off shows a warning hint and saves verifySsl: false", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("section-advanced-trigger").click();

    const checkName = `E2E VerifySsl Off ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/verify-ssl-off");

    await page.getByTestId("check-verify-ssl-switch").click();
    await expect(page.getByTestId("check-verify-ssl-warning")).toBeVisible();
    await expect(page.getByTestId("check-verify-ssl-warning")).toContainText(
      "Certificate errors will be ignored",
    );

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.verifySsl).toBe(false);
    expect(created.config).not.toHaveProperty("followRedirects");

    // Reload the edit page — the switch state and warning persist. The
    // Advanced section auto-opens here (it holds a non-default value), so no
    // click is needed to reveal it — unlike the fresh "new check" page above.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-verify-ssl-switch")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    await expect(page.getByTestId("check-verify-ssl-warning")).toBeVisible();

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("turning followRedirects off saves followRedirects: false with no warning hint", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("section-advanced-trigger").click();

    const checkName = `E2E FollowRedirects Off ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/follow-redirects-off");

    await page.getByTestId("check-follow-redirects-switch").click();
    // followRedirects off is not a security-relevant hint — verifySsl's
    // warning must not appear because of it.
    await expect(page.getByTestId("check-verify-ssl-warning")).toHaveCount(0);

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.followRedirects).toBe(false);
    expect(created.config).not.toHaveProperty("verifySsl");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
