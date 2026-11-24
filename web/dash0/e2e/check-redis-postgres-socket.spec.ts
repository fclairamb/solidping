import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-11-24-03: with the server allowlist set
// (SP_CHECKERS_UNIX_SOCKET_PATHS=/tmp/solidping-e2e-sockets/ in CI), the redis
// and postgres forms offer a "Unix socket" target and save the path as
// `socket`, without host or port.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Redis and Postgres checks on a Unix socket", () => {
  test("creates a redis check in socket mode", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=redis");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("redis-target-mode").click();
    await page.getByTestId("redis-target-mode-socket").click();
    await expect(page.getByTestId("check-host-input")).toHaveCount(0);

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Redis socket ${Date.now()}`);

    // Outside the allowlist: the server's finding lands on the socket input.
    await page
      .getByTestId("redis-socket-path")
      .fill("/run/elsewhere/redis.sock");
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("redis-socket-error")).toContainText(
      "checkers.unix_socket_paths",
    );

    const socket = `/tmp/solidping-e2e-sockets/redis-${Date.now()}.sock`;
    await page.getByTestId("redis-socket-path").fill(socket);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
      {
        headers: { Authorization: `Bearer ${token}` },
      },
    );
    expect(resp.status()).toBe(200);
    const created = await resp.json();
    expect(created.type).toBe("redis");
    expect(created.config.socket).toBe(socket);
    expect(created.config.host ?? "").toBe("");

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("redis-socket-path")).toHaveValue(socket);
  });

  test("creates a postgres check in socket mode", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=postgresql");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("postgres-target-mode").click();
    await page.getByTestId("postgres-target-mode-socket").click();
    await expect(page.getByTestId("check-host-input")).toHaveCount(0);

    await page
      .getByTestId("check-name-input")
      .fill(`E2E Postgres socket ${Date.now()}`);
    await page.getByTestId("check-username-input").fill("monitor");

    const socket = `/tmp/solidping-e2e-sockets/.s.PGSQL.${Date.now()}`;
    await page.getByTestId("postgres-socket-path").fill(socket);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
      {
        headers: { Authorization: `Bearer ${token}` },
      },
    );
    const created = await resp.json();
    expect(created.type).toBe("postgresql");
    expect(created.config.socket).toBe(socket);
    expect(created.config.host ?? "").toBe("");
  });
});
