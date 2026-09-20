import { test, expect, type Page } from "./fixtures";

// §5 of spec 2026-09-19-05, mirroring slack-socket-mode: the instance-level
// Server → Discord page. `server.discord.tsx` shipped without any E2E cover,
// which is exactly the shape of gap that let the Slack readers rot around the
// token move (spec 2026-09-18-02).
//
// The load-bearing assertion is the LAST one: the keys the UI writes must be the
// keys the backend reads. A `discord.*` namespace instead of `auth.discord.*`
// persists rows that silently do nothing, and every other test here would still
// pass.

const SYSTEM_PARAMS_URL = "**/api/v1/system/parameters";
const GATEWAY_STATUS_URL = "**/api/v1/integrations/discord/gateway/status";

// The canonical keys, from server/internal/systemconfig/systemconfig.go.
const KEY_ENABLED = "auth.discord.enabled";
const KEY_PUBLIC_KEY = "auth.discord.public_key";
const KEY_GATEWAY_ENABLED = "auth.discord.gateway_enabled";

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

async function mockGatewayStatus(page: Page, body: Record<string, unknown>) {
  await page.route(GATEWAY_STATUS_URL, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
}

test.describe("Discord Gateway settings", () => {
  test("renders the not-enabled hint when Discord is disabled", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // auth.discord.enabled absent (= falsy).
    await mockSystemParameters(page, []);
    await mockGatewayStatus(page, { enabled: false, connected: false });

    await page.goto("orgs/test/server/discord");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-not-enabled")).toBeVisible();
    // Nothing editable: offering a public-key field on an instance where Discord
    // is off invites saving settings nothing reads.
    await expect(page.getByTestId("discord-public-key-input")).toHaveCount(0);
    await expect(page.getByTestId("discord-save")).toHaveCount(0);
  });

  test("shows the Connected badge and the server count", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = new Date().toISOString();

    await mockSystemParameters(page, [
      { key: KEY_ENABLED, value: true, secret: false, updatedAt: now },
      { key: KEY_GATEWAY_ENABLED, value: true, secret: false, updatedAt: now },
      { key: KEY_PUBLIC_KEY, value: "abcd1234", secret: false, updatedAt: now },
    ]);
    await mockGatewayStatus(page, {
      enabled: true,
      connected: true,
      guildCount: 2,
      lastConnectedAt: now,
      lastError: "",
    });

    await page.goto("orgs/test/server/discord");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-status-badge")).toHaveText(
      /connected/i,
    );
    await expect(page.getByTestId("discord-status-guilds")).toContainText("2");
    await expect(page.getByTestId("discord-status-last-error")).toHaveText(
      /none/i,
    );

    // The MESSAGE_CONTENT hint is the one piece of operator knowledge that
    // cannot be derived from the status: the bot connects and reports Connected
    // without the intent, and every inbound message arrives empty.
    await expect(page.getByTestId("discord-intent-hint")).toBeVisible();
  });

  test("shows Disconnected and surfaces the last error", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = new Date().toISOString();

    await mockSystemParameters(page, [
      { key: KEY_ENABLED, value: true, secret: false, updatedAt: now },
      { key: KEY_GATEWAY_ENABLED, value: true, secret: false, updatedAt: now },
    ]);
    await mockGatewayStatus(page, {
      enabled: true,
      connected: false,
      guildCount: 0,
      lastError: "websocket: close 4004 authentication failed",
    });

    await page.goto("orgs/test/server/discord");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-status-badge")).toHaveText(
      /disconnected/i,
    );
    await expect(page.getByTestId("discord-status-last-error")).toContainText(
      "4004",
    );
  });

  test("the settings it saves are the keys the backend reads", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const now = new Date().toISOString();

    let rows: ParamRow[] = [
      { key: KEY_ENABLED, value: true, secret: false, updatedAt: now },
      {
        key: KEY_GATEWAY_ENABLED,
        value: false,
        secret: false,
        updatedAt: now,
      },
      { key: KEY_PUBLIC_KEY, value: "", secret: false, updatedAt: now },
    ];

    const puts: Array<{ key: string; body: Record<string, unknown> }> = [];

    await page.route(SYSTEM_PARAMS_URL, async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill(paramsResponse(rows));

        return;
      }

      await route.continue();
    });

    await page.route("**/api/v1/system/parameters/*", async (route) => {
      if (route.request().method() !== "PUT") {
        await route.continue();

        return;
      }

      const key = decodeURIComponent(route.request().url().split("/").pop() ?? "");
      const body = route.request().postDataJSON() as Record<string, unknown>;
      puts.push({ key, body });

      rows = rows
        .filter((row) => row.key !== key)
        .concat({
          key,
          value: body.value,
          secret: body.secret === true,
          updatedAt: new Date().toISOString(),
        });

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          key,
          value: body.value,
          secret: body.secret === true,
          updatedAt: new Date().toISOString(),
        }),
      });
    });

    await mockGatewayStatus(page, { enabled: true, connected: false });

    await page.goto("orgs/test/server/discord");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("discord-public-key-input").fill("00112233aabbccdd");
    await page.getByTestId("discord-gateway-enabled").click();
    await page.getByTestId("discord-save").click();

    // The public key is deliberately NOT secret: an operator has to read it back
    // to confirm it matches the one Discord prints on the application page.
    await expect
      .poll(() => puts.find((put) => put.key === KEY_PUBLIC_KEY)?.body.value)
      .toBe("00112233aabbccdd");
    expect(puts.find((put) => put.key === KEY_PUBLIC_KEY)?.body.secret).not.toBe(
      true,
    );

    await expect
      .poll(() => puts.find((put) => put.key === KEY_GATEWAY_ENABLED)?.body.value)
      .toBe(true);

    // The regression guard: nothing may be written to a bare `discord.*`
    // namespace the backend never loads.
    expect(puts.find((put) => put.key === "discord.public_key")).toBeUndefined();
    expect(
      puts.find((put) => put.key === "discord.gateway_enabled"),
    ).toBeUndefined();
  });
});
