import { test, expect } from "./fixtures";

// Honor E2E_BASE_URL (side-car test server) like playwright.config.ts does;
// fall back to the CI default.
const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";

test.describe("Web Push Foundation", () => {
  test("service worker registers at /dash0/sw.js", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Grant notification permission so the SW has the right context.
    await page.context().grantPermissions(["notifications"]);

    // Navigate to the org dashboard.
    await page.goto(`${API_BASE}/dash0/orgs/test`);
    await page.waitForLoadState("networkidle");

    // Assert the service worker registered for the /dash0/sw.js scope.
    const registration = await page.evaluate(() =>
      navigator.serviceWorker.getRegistration("/dash0/sw.js"),
    );
    expect(registration).toBeTruthy();
  });

  test("VAPID public key endpoint returns a non-empty string", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const resp = await page.request.post(
      `${API_BASE}/api/v1/auth/login`,
      { data: { org: "test", email: "test@test.com", password: "test" } },
    );
    const { accessToken } = await resp.json();

    const keyResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/webpush/vapid-public-key`,
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );
    expect(keyResp.ok()).toBeTruthy();

    const body = await keyResp.json();
    expect(typeof body.data.publicKey).toBe("string");
    expect(body.data.publicKey.length).toBeGreaterThan(0);
  });
});
