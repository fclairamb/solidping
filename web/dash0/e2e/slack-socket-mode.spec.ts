import { test, expect, type Page } from "./fixtures";

const SYSTEM_PARAMS_URL = "**/api/v1/system/parameters";
const SOCKET_STATUS_URL = "**/api/v1/integrations/slack/socket/status";

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

    // Mock system parameters so slack.enabled is absent (= falsy)
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
      { key: "slack.enabled", value: true, secret: false, updatedAt: now },
      {
        key: "slack.socket_mode_enabled",
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

    // Assert PUT calls
    const tokenPut = puts.find((p) => p.key === "slack.app_token");
    expect(tokenPut, "expected PUT for slack.app_token").toBeDefined();
    expect(tokenPut?.body.value).toBe("xapp-test-token");
    expect(tokenPut?.body.secret).toBe(true);

    const socketPut = puts.find((p) => p.key === "slack.socket_mode_enabled");
    expect(socketPut, "expected PUT for slack.socket_mode_enabled").toBeDefined();
    expect(socketPut?.body.value).toBe(true);

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
      { key: "slack.enabled", value: true, secret: false, updatedAt: now },
      {
        key: "slack.socket_mode_enabled",
        value: true,
        secret: false,
        updatedAt: now,
      },
      {
        key: "slack.app_token",
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
      { key: "slack.enabled", value: true, secret: false, updatedAt: now },
      {
        key: "slack.socket_mode_enabled",
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
