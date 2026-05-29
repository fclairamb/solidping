import { test, expect, type Page } from "./fixtures";

const API_BASE = "http://localhost:4000";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createWebhookChannel(
  page: Page,
  token: string,
  url: string,
): Promise<{ uid: string; name: string }> {
  const name = `E2E SW Hook ${Date.now()}`;
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/channels`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { type: "webhook", name, settings: { url } },
  });
  const body = await resp.json();
  return { uid: body.uid, name };
}

async function deleteChannel(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/channels/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Standard Webhooks channel form", () => {
  test("signing secret is visible and copyable", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Grant clipboard permissions so the Copy button can write.
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);

    const { uid } = await createWebhookChannel(
      page,
      token,
      "https://example.com/hook",
    );

    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");

    const secretInput = page.getByTestId("webhook-signing-secret");
    await expect(secretInput).toBeVisible();
    const secretValue = await secretInput.inputValue();
    expect(secretValue).toMatch(/^whsec_/);

    // Copy and read back from the clipboard.
    await page.getByTestId("webhook-copy-secret").click();
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    expect(clip).toBe(secretValue);

    await deleteChannel(page, token, uid);
  });

  test("rotate generates a new secret and shows the rotation banner", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const { uid } = await createWebhookChannel(
      page,
      token,
      "https://example.com/hook",
    );

    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");

    const secretInput = page.getByTestId("webhook-signing-secret");
    await expect(secretInput).toBeVisible();
    const before = await secretInput.inputValue();

    await page.getByTestId("webhook-rotate-secret").click();

    // The displayed secret changes once the mutation invalidates the query.
    await expect(async () => {
      const after = await secretInput.inputValue();
      expect(after).not.toBe(before);
      expect(after).toMatch(/^whsec_/);
    }).toPass();

    // The rotation-in-progress banner appears (previous secret in grace window).
    await expect(page.getByTestId("webhook-rotation-banner")).toBeVisible();

    await deleteChannel(page, token, uid);
  });

  test("send test renders a success badge (mocked endpoint)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const { uid } = await createWebhookChannel(
      page,
      token,
      "https://example.com/hook",
    );

    // Intercept the test-delivery call so the assertion does not depend on a
    // real outbound HTTP request.
    await page.route(
      `**/api/v1/orgs/test/integrations/${uid}/test`,
      async (route) => {
        if (route.request().method() === "POST") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              success: true,
              statusCode: 200,
              durationMs: 42,
            }),
          });
          return;
        }
        await route.continue();
      },
    );

    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("webhook-send-test").click();

    const badge = page.getByTestId("webhook-test-result");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText("200 OK");
    await expect(badge).toContainText("42 ms");

    await deleteChannel(page, token, uid);
  });

  test("send test renders a failure badge (mocked endpoint)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const { uid } = await createWebhookChannel(
      page,
      token,
      "https://example.com/hook",
    );

    await page.route(
      `**/api/v1/orgs/test/integrations/${uid}/test`,
      async (route) => {
        if (route.request().method() === "POST") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              success: false,
              statusCode: 500,
              durationMs: 87,
              error: "remote refused",
            }),
          });
          return;
        }
        await route.continue();
      },
    );

    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("webhook-send-test").click();

    const badge = page.getByTestId("webhook-test-result");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText("500");
    await expect(badge).toContainText("remote refused");

    await deleteChannel(page, token, uid);
  });
});
