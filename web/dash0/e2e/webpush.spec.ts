import { test, expect, API_BASE } from "./fixtures";

test.describe("Web Push Foundation", () => {
  // Service worker registration at ${DASH_BASE}/sw.js (scope ${DASH_BASE}/) is
  // asserted by service-worker-scope.spec.ts.

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
