import { test, expect, type Page } from "./fixtures";

const API_BASE = "http://localhost:4000";
const SYSTEM_PARAMS_URL = "**/api/v1/system/parameters";
const SOCKET_STATUS_URL = "**/api/v1/integrations/slack/socket/status";

// These are the canonical keys the backend reads
// (server/internal/systemconfig/systemconfig.go) and the dashboard writes
// (server.slack.tsx KEY_* constants). The mocked tests below assert PUTs to
// these exact strings so a front/back key drift can no longer pass CI while the
// store is mocked away — and the un-mocked round-trip test verifies the real
// backend persists them.
const KEY_ENABLED = "auth.slack.enabled";
const KEY_SOCKET_ENABLED = "auth.slack.socket_mode_enabled";
const KEY_APP_TOKEN = "auth.slack.app_token";

interface ParamRow {
  key: string;
  value: unknown;
  secret: boolean;
  updatedAt: string;
}

function paramsResponse(rows: ParamRow[]) {
  return {
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ data: rows }),
  };
}

async function mockSystemParameters(page: Page, rows: ParamRow[]) {
  await page.route(SYSTEM_PARAMS_URL, async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill(paramsResponse(rows));
      return;
    }
    await route.continue();
  });
}

async function mockSocketStatus(
  page: Page,
  body: Record<string, unknown>,
) {
  await page.route(SOCKET_STATUS_URL, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
}

test.describe("Slack Socket Mode settings", () => {
  test("renders not-enabled hint when Slack is disabled", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Mock system parameters so auth.slack.enabled is absent (= falsy)
    await mockSystemParameters(page, []);
    await mockSocketStatus(page, { enabled: false, connected: false });

    await page.goto("orgs/test/server/slack");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slack-not-enabled")).toBeVisible();
    await expect(page.getByTestId("slack-app-token-input")).toHaveCount(0);
    await expect(page.getByTestId("slack-socket-save")).toHaveCount(0);
  });

  test("can enable socket mode and save token", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = new Date().toISOString();

    let rows: ParamRow[] = [
      { key: KEY_ENABLED, value: true, secret: false, updatedAt: now },
      {
        key: KEY_SOCKET_ENABLED,
        value: false,
        secret: false,
        updatedAt: now,
      },
    ];

    // Capture PUT bodies so the test can assert on them.
    const puts: Array<{ key: string; body: Record<string, unknown> }> = [];

    await page.route(SYSTEM_PARAMS_URL, async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill(paramsResponse(rows));
        return;
      }
      await route.continue();
    });

    await page.route(
      "**/api/v1/system/parameters/*",
      async (route) => {
        if (route.request().method() !== "PUT") {
          await route.continue();
          return;
        }
        const url = route.request().url();
        const key = decodeURIComponent(url.split("/").pop() || "");
        const body = route.request().postDataJSON() as Record<string, unknown>;
        puts.push({ key, body });

        // Update the in-memory rows so the refetched list reflects the change.
        const valueAsString = String(body.value ?? "");
        const isSecret = body.secret === true;
        rows = rows.filter((r) => r.key !== key).concat({
          key,
          value: isSecret ? "******" : body.value,
          secret: isSecret,
          updatedAt: new Date().toISOString(),
        });

        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            key,
            value: isSecret ? "******" : body.value,
            secret: isSecret,
            updatedAt: new Date().toISOString(),
          }),
        });
        // Touch the lint variable to silence unused warnings
        void valueAsString;
      },
    );

    await mockSocketStatus(page, { enabled: true, connected: false });

    await page.goto("orgs/test/server/slack");
    await page.waitForLoadState("networkidle");

    // Toggle Socket Mode ON
    await page.getByTestId("slack-socket-mode-enabled").click();

    // Enter an xapp- token
    await page.getByTestId("slack-app-token-input").fill("xapp-test-token");

    // Save
    await page.getByTestId("slack-socket-save").click();

    // Wait for refetch to bring back masked field
    await expect(page.getByTestId("slack-app-token-masked")).toBeVisible({
      timeout: 5000,
    });

    // Assert PUT calls target the keys the BACKEND reads (auth.slack.*), not
    // the bare slack.* namespace that silently did nothing.
    const tokenPut = puts.find((p) => p.key === KEY_APP_TOKEN);
    expect(tokenPut, `expected PUT for ${KEY_APP_TOKEN}`).toBeDefined();
    expect(tokenPut?.body.value).toBe("xapp-test-token");
    expect(tokenPut?.body.secret).toBe(true);

    const socketPut = puts.find((p) => p.key === KEY_SOCKET_ENABLED);
    expect(socketPut, `expected PUT for ${KEY_SOCKET_ENABLED}`).toBeDefined();
    expect(socketPut?.body.value).toBe(true);

    // Guard against regression: nothing must be written to the old bare keys.
    expect(puts.find((p) => p.key === "slack.app_token")).toBeUndefined();
    expect(
      puts.find((p) => p.key === "slack.socket_mode_enabled"),
    ).toBeUndefined();

    // The raw token must not appear in the DOM after save.
    const masked = page.getByTestId("slack-app-token-masked");
    await expect(masked).toBeVisible();
    await expect(masked).toHaveValue("******");
    await expect(page.locator("body")).not.toContainText("xapp-test-token");
  });

  test("status card shows Connected badge and team count", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = new Date().toISOString();

    await mockSystemParameters(page, [
      { key: KEY_ENABLED, value: true, secret: false, updatedAt: now },
      {
        key: KEY_SOCKET_ENABLED,
        value: true,
        secret: false,
        updatedAt: now,
      },
      {
        key: KEY_APP_TOKEN,
        value: "******",
        secret: true,
        updatedAt: now,
      },
    ]);

    await mockSocketStatus(page, {
      enabled: true,
      connected: true,
      teamCount: 2,
      lastConnectedAt: now,
    });

    await page.goto("orgs/test/server/slack");
    await page.waitForLoadState("networkidle");

    const badge = page.getByTestId("slack-status-badge");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText(/connected/i);

    await expect(page.getByTestId("slack-status-teams")).toContainText("2");
  });

  test("status card shows Disconnected badge and last error", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = new Date().toISOString();

    await mockSystemParameters(page, [
      { key: KEY_ENABLED, value: true, secret: false, updatedAt: now },
      {
        key: KEY_SOCKET_ENABLED,
        value: true,
        secret: false,
        updatedAt: now,
      },
    ]);

    await mockSocketStatus(page, {
      enabled: true,
      connected: false,
      lastError: "network timeout",
    });

    await page.goto("orgs/test/server/slack");
    await page.waitForLoadState("networkidle");

    const badge = page.getByTestId("slack-status-badge");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText(/disconnected/i);

    await expect(page.getByTestId("slack-status-last-error")).toContainText(
      "network timeout",
    );
  });
});

// -------------------------------------------------------------------------
// Un-mocked round-trip: this is the test that would have caught the original
// bug. A purely-mocked suite structurally cannot detect a front/back key
// mismatch — the store is faked away. Here we drive the real UI against the
// live make dev-test backend and assert, via the real GET, that the page
// persisted the keys the BACKEND reads (auth.slack.*). Requires a
// SP_RUNMODE=test backend on :4000 (test org, test@test.com/test).
// -------------------------------------------------------------------------

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken as string;
}

async function setParam(
  page: Page,
  token: string,
  key: string,
  value: unknown,
  secret = false,
): Promise<void> {
  const resp = await page.request.put(
    `${API_BASE}/api/v1/system/parameters/${key}`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { value, secret },
    },
  );
  expect(resp.ok(), `PUT ${key} should succeed`).toBeTruthy();
}

async function deleteParam(
  page: Page,
  token: string,
  key: string,
): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/system/parameters/${key}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

interface ApiParamRow {
  key: string;
  value: unknown;
  secret: boolean;
}

async function listParams(page: Page, token: string): Promise<ApiParamRow[]> {
  const resp = await page.request.get(`${API_BASE}/api/v1/system/parameters`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(resp.ok(), "GET system/parameters should succeed").toBeTruthy();
  return ((await resp.json()).data ?? []) as ApiParamRow[];
}

test.describe("Slack Socket Mode key contract (un-mocked)", () => {
  // Restore the shared test org: turn Socket Mode back off and drop the token
  // so later suites are unaffected. We leave auth.slack.enabled to whatever it
  // was; the per-test setup re-asserts it.
  test.afterEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteParam(authenticatedPage, token, KEY_SOCKET_ENABLED);
    await deleteParam(authenticatedPage, token, KEY_APP_TOKEN);
  });

  test("saving in the UI persists auth.slack.* keys the backend reads", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Un-gate the page exactly as an operator would: enable Slack via the same
    // key the Authentication settings page writes and the backend reads.
    await setParam(page, token, KEY_ENABLED, true);

    await page.goto("orgs/test/server/slack");
    await page.waitForLoadState("networkidle");

    // The FORM must render (not the "not enabled" hint) — this alone fails if
    // the gate reads the wrong key.
    await expect(page.getByTestId("slack-socket-save")).toBeVisible();
    await expect(page.getByTestId("slack-not-enabled")).toHaveCount(0);

    // Toggle Socket Mode on, enter an xapp- token, save.
    const uniqueToken = `xapp-e2e-${Date.now()}`;
    await page.getByTestId("slack-socket-mode-enabled").click();
    await page.getByTestId("slack-app-token-input").fill(uniqueToken);
    await page.getByTestId("slack-socket-save").click();

    // After save the token field re-renders masked (no raw token in the DOM).
    await expect(page.getByTestId("slack-app-token-masked")).toBeVisible({
      timeout: 5000,
    });
    await expect(page.locator("body")).not.toContainText(uniqueToken);

    // The real contract check: the backend now holds the values under the keys
    // it actually loads into cfg.Slack on startup.
    const params = await listParams(page, token);
    const socket = params.find((p) => p.key === KEY_SOCKET_ENABLED);
    expect(socket, `expected ${KEY_SOCKET_ENABLED} to be persisted`).toBeDefined();
    expect(socket?.value).toBe(true);

    const appToken = params.find((p) => p.key === KEY_APP_TOKEN);
    expect(appToken, `expected ${KEY_APP_TOKEN} to be persisted`).toBeDefined();
    expect(appToken?.secret).toBe(true);

    // The bug-encoding keys must never be written by the page.
    expect(params.find((p) => p.key === "slack.socket_mode_enabled")).toBeUndefined();
    expect(params.find((p) => p.key === "slack.app_token")).toBeUndefined();
  });
});
