import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-11-24-04: with the server allowlist set
// (SP_CHECKERS_UNIX_SOCKET_PATHS=/tmp/solidping-e2e-sockets/ in CI), the mysql,
// mongodb and smtp forms offer a "Unix socket" target and save the path as
// `socket`, without host or port.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

const cases = [
  {
    type: "mysql",
    prefix: "mysql",
    socket: () => `/tmp/solidping-e2e-sockets/mysql-${Date.now()}.sock`,
    fill: async (page: Page) =>
      page.getByTestId("check-username-input").fill("monitor"),
  },
  {
    type: "mongodb",
    prefix: "mongodb",
    socket: () => `/tmp/solidping-e2e-sockets/mongodb-${Date.now()}.sock`,
    fill: async () => {},
  },
  {
    type: "smtp",
    prefix: "smtp",
    socket: () => `/tmp/solidping-e2e-sockets/smtp-${Date.now()}.sock`,
    fill: async () => {},
  },
];

test.describe("MySQL, MongoDB and SMTP checks on a Unix socket", () => {
  for (const { type, prefix, socket: makeSocket, fill } of cases) {
    test(`creates a ${type} check in socket mode`, async ({
      authenticatedPage,
    }) => {
      const page = authenticatedPage;
      const token = await getAuthToken(page);

      await page.goto(`orgs/test/checks/new?checkType=${type}`);
      await expect(page.getByTestId("check-name-input")).toBeVisible();

      await page.getByTestId(`${prefix}-target-mode`).click();
      await page.getByTestId(`${prefix}-target-mode-socket`).click();
      await expect(page.getByTestId("check-host-input")).toHaveCount(0);

      await page
        .getByTestId("check-name-input")
        .fill(`E2E ${type} socket ${Date.now()}`);
      await fill(page);

      // Outside the allowlist: the server's finding lands on the socket input.
      await page
        .getByTestId(`${prefix}-socket-path`)
        .fill(`/run/elsewhere/${type}.sock`);
      await page.getByTestId("check-submit-button").click();
      await expect(page.getByTestId(`${prefix}-socket-error`)).toContainText(
        "checkers.unix_socket_paths",
      );

      const socket = makeSocket();
      await page.getByTestId(`${prefix}-socket-path`).fill(socket);
      await page.getByTestId("check-submit-button").click();
      await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
      const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

      const resp = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      expect(resp.status()).toBe(200);
      const created = await resp.json();
      expect(created.type).toBe(type);
      expect(created.config.socket).toBe(socket);
      expect(created.config.host ?? "").toBe("");

      await page.goto(`orgs/test/checks/${uid}/edit`);
      await expect(page.getByTestId(`${prefix}-socket-path`)).toHaveValue(
        socket,
      );
    });
  }
});
