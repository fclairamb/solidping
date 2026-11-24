import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-11-24-01 (Unix socket targets): with the server
// allowlist set (SP_CHECKERS_UNIX_SOCKET_PATHS=/tmp/solidping-e2e-sockets/ in
// CI, see .github/workflows/ci.yml), the tcp form offers a "Unix socket" target
// and saves the path as `socket`, without host or port. A path outside the
// allowlist is refused on the socket input.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("TCP check on a Unix socket", () => {
  test("creates a tcp check in socket mode and saves it with `socket`", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("tcp-target-mode").click();
    await page.getByTestId("tcp-target-mode-socket").click();
    await expect(page.getByTestId("check-host-input")).toHaveCount(0);

    const checkName = `E2E TCP socket ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);

    // Outside the allowlist: the server's finding lands on the socket input.
    await page.getByTestId("tcp-socket-path").fill("/run/elsewhere/app.sock");
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("tcp-socket-error")).toContainText(
      "checkers.unix_socket_paths",
    );

    const socket = `/tmp/solidping-e2e-sockets/app-${Date.now()}.sock`;
    await page.getByTestId("tcp-socket-path").fill(socket);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(resp.status()).toBe(200);
    const created = await resp.json();
    expect(created.type).toBe("tcp");
    expect(created.config.socket).toBe(socket);
    expect(created.config.host ?? "").toBe("");
    expect(created.config.port ?? 0).toBe(0);

    // Editing shows the stored socket.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("tcp-socket-path")).toHaveValue(socket);
  });
});
