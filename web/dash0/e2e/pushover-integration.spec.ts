import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-10-08-03: a Pushover integration created from the dashboard must
// store the canonical user_key / api_token pair (encrypted) and reach the
// sender, and a missing setting must be reported as such, not as a generic
// delivery failure.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteIntegration(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Pushover integration", () => {
  test("the form stores user_key/api_token encrypted and Send test reaches the sender", async ({
    authenticatedPage,
  }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/integrations/new?type=pushover");
    await page.waitForLoadState("networkidle");

    await page.locator("#ch-name").fill(`E2E Pushover ${Date.now()}`);
    await page.locator("#ch-pushover-user").fill("uE2EUserKey000000000000000000000");
    await page.locator("#ch-pushover-token").fill("aE2EApiToken00000000000000000000");

    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/orgs\/test\/integrations\/[0-9a-f-]{36}$/.test(url.pathname),
    );
    const uid = new URL(page.url()).pathname.split("/").pop() as string;

    const integration = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .then((r) => r.json());

    expect(integration.settingsPrivateKeys).toEqual(
      expect.arrayContaining(["api_token", "user_key"]),
    );
    for (const key of ["user", "token", "user_key", "api_token"]) {
      expect(integration.settings ?? {}).not.toHaveProperty(key);
    }

    // Unmocked: the real test endpoint runs the real sender. Whatever the
    // network says about these fake credentials, the sender must find both
    // settings, so the result is never the "missing setting" one.
    const testResponse = page.waitForResponse(
      (r) =>
        r.url().includes(`/integrations/${uid}/test`) &&
        r.request().method() === "POST",
      { timeout: 60_000 },
    );
    await page.getByTestId("webhook-send-test").click();
    const result = await (await testResponse).json();
    expect(result.code).toBeUndefined();
    expect(result.error ?? "").not.toContain("not configured");

    const badge = page.getByTestId("webhook-test-result");
    await expect(badge).toBeVisible();
    await expect(badge).not.toContainText("missing");

    await deleteIntegration(page, token, uid);
  });

  test("a missing API token is reported as a configuration error", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const created = await page.request
      .post(`${API_BASE}/api/v1/orgs/test/integrations`, {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "pushover",
          name: `E2E Pushover no token ${Date.now()}`,
          settings: { user_key: "uE2EUserKey000000000000000000000" },
        },
      })
      .then((r) => r.json());

    await page.goto(`orgs/test/integrations/${created.uid}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("webhook-send-test").click();

    const badge = page.getByTestId("webhook-test-result");
    await expect(badge).toHaveText("This integration is missing its API token");

    await deleteIntegration(page, token, created.uid);
  });
});
