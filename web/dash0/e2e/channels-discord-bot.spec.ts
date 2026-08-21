import { test, expect } from "./fixtures";

// The Discord settings panel replaced the bare webhook-URL field. It has to
// serve BOTH modes at once, because both are live and which one an integration
// is in is a property of its data:
//
//   - a legacy webhook-only integration must still show its webhook URL, and
//     must never call the bot-only /discord/destinations endpoint;
//   - a bot-installed integration must show the guild, the channel picker and
//     the mention / comment-ingestion switches.
const FAKE_AUTHORIZE_URL =
  "https://discord.com/oauth2/authorize?client_id=fake&state=fake-state";

const LEGACY_UID = "33333333-3333-3333-3333-333333333333";
const BOT_UID = "44444444-4444-4444-4444-444444444444";

/** Stubs GET on one integration with the given settings blob. */
async function stubIntegration(
  page: import("@playwright/test").Page,
  uid: string,
  settings: Record<string, unknown>,
) {
  await page.route(
    `**/api/v1/orgs/test/integrations/${uid}`,
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();

        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid,
          type: "discord",
          name: "acme discord",
          enabled: true,
          isDefault: false,
          settings,
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        }),
      });
    },
  );
}

test.describe("Discord bot settings panel", () => {
  test("legacy webhook integration keeps its URL and never calls destinations", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubIntegration(page, LEGACY_UID, {
      webhook_url: "https://discord.com/api/webhooks/1/acme-legacy",
    });

    let destinationsCalled = false;
    await page.route(
      `**/api/v1/orgs/test/channels/${LEGACY_UID}/discord/destinations`,
      async (route) => {
        destinationsCalled = true;
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            title: "Discord server is not connected",
            code: "CHANNEL_NOT_CONNECTED",
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${LEGACY_UID}`);
    await page.waitForLoadState("networkidle");

    // The not-connected block explains the bot, and the webhook field is still
    // there with the stored URL — this is the regression guard for every
    // pre-bot Discord integration.
    await expect(page.getByTestId("discord-not-connected")).toBeVisible();
    await expect(
      page.locator(
        'input[value="https://discord.com/api/webhooks/1/acme-legacy"]',
      ),
    ).toHaveCount(1);

    // No bot-only UI, and no call to the bot-only endpoint.
    await expect(page.getByTestId("discord-channel-combobox")).toHaveCount(0);
    expect(destinationsCalled).toBe(false);
  });

  test("install CTA mints an org-scoped install URL for this channel", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubIntegration(page, LEGACY_UID, { webhook_url: "" });

    await page.goto(`orgs/test/integrations/${LEGACY_UID}`);
    await page.waitForLoadState("networkidle");

    const installButton = page.getByTestId("discord-install");
    await expect(installButton).toBeVisible();
    await expect(installButton).toHaveText(/install discord bot/i);

    let installUrlBody: Record<string, unknown> = {};
    await page.route(
      "**/api/v1/orgs/test/integrations/discord/install-url",
      async (route) => {
        expect(route.request().method()).toBe("POST");
        installUrlBody = route.request().postDataJSON() as Record<
          string,
          unknown
        >;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ url: FAKE_AUTHORIZE_URL }),
        });
      },
    );
    await page.route("https://discord.com/oauth2/**", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/html",
        body: "<html><body>fake discord authorize page</body></html>",
      });
    });

    await installButton.click();

    // The channel uid travels with the request so the callback updates THIS
    // integration instead of creating a second one.
    await expect.poll(() => installUrlBody.channelUid).toBe(LEGACY_UID);
    await page.waitForURL(FAKE_AUTHORIZE_URL);
  });

  test("bot-installed integration shows the channel picker and switches", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubIntegration(page, BOT_UID, {
      guild_id: "G-ACME",
      guild_name: "acme",
      channel_id: "C-ALERTS",
      channel_name: "alerts",
      mention_on_call: true,
      comment_ingestion: "explicit",
    });

    await page.route(
      `**/api/v1/orgs/test/channels/${BOT_UID}/discord/destinations`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            guildId: "G-ACME",
            guildName: "acme",
            connected: true,
            channels: [
              { id: "C-ALERTS", name: "alerts", type: 0 },
              { id: "C-GENERAL", name: "general", type: 0 },
            ],
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${BOT_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-connected")).toBeVisible();
    await expect(page.getByTestId("discord-not-connected")).toHaveCount(0);

    // The guild is named on the panel, and the picker shows the selected channel.
    await expect(
      page.getByTestId("discord-connected").getByText("Server:"),
    ).toBeVisible();

    const combobox = page.getByTestId("discord-channel-combobox");
    await expect(combobox).toBeVisible();
    await expect(combobox).toContainText("#alerts");

    // Both switches render, with the stored values.
    await expect(page.getByTestId("discord-mention-on-call")).toHaveAttribute(
      "data-state",
      "checked",
    );
    await expect(
      page.getByTestId("discord-comment-ingestion"),
    ).toHaveAttribute("data-state", "unchecked");

    // Picking another channel updates the trigger label.
    await combobox.click();
    await page.getByTestId("discord-channel-option-C-GENERAL").click();
    await expect(combobox).toContainText("#general");
  });
});
