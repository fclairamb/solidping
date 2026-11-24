import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-11-24-02: with the server allowlist set
// (SP_CHECKERS_UNIX_SOCKET_PATHS=/tmp/solidping-e2e-sockets/ in CI), the http
// form offers a "Unix socket path" input under Advanced and saves it as
// `unix_socket`.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("HTTP check over a Unix socket", () => {
  test("creates an http check with a socket path", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await page.getByTestId("check-name-input").fill(`E2E HTTP socket ${Date.now()}`);
    await page.getByTestId("check-url-input").fill("http://localhost/health");

    await page.getByTestId("section-advanced-trigger").click();
    const socket = `/tmp/solidping-e2e-sockets/http-${Date.now()}.sock`;
    await page.getByTestId("http-unix-socket").fill(socket);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(resp.status()).toBe(200);
    const created = await resp.json();
    expect(created.type).toBe("http");
    expect(created.config.unix_socket).toBe(socket);
  });
});
