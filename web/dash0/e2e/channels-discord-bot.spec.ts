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

/**
 * Stubs GET /api/v1/config with a given Discord bot capability.
 *
 * The install button now renders ONLY when the instance reports a fully
 * configured bot, because that is the same predicate the backend mounts the
 * install routes on. Production had Discord "enabled" with just the client
 * id/secret pair — enough for login, not for the bot — and the button was
 * offered anyway, sending users to an install that could not complete
 * (spec 2026-09-19-01). So every test here has to say which instance it is.
 */
async function stubPublicConfig(
  page: import("@playwright/test").Page,
  botEnabled: boolean,
) {
  await page.route("**/api/v1/config", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        posthog: { enabled: false },
        whatsapp: { enabled: false },
        telegram: { enabled: false },
        sms: { enabled: false, voiceEnabled: false },
        demo: { enabled: false },
        discord: { botEnabled },
      }),
    });
  });
}

test.describe("Discord bot settings panel", () => {
  test("legacy webhook integration keeps its URL and never calls destinations", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubPublicConfig(page, true);
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

  test("an empty webhook field is hidden when the bot can be installed, and shown when it cannot", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Bot available + nothing stored: installing is the answer, so the empty
    // legacy field is clutter sitting next to the install button.
    await stubPublicConfig(page, true);
    await stubIntegration(page, LEGACY_UID, { webhook_url: "" });

    await page.goto(`orgs/test/integrations/${LEGACY_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-not-connected")).toBeVisible();
    await expect(page.getByTestId("discord-install")).toBeVisible();
    await expect(page.getByLabel(/webhook url/i)).toHaveCount(0);

    // Positive control: the SAME empty integration on an instance with no bot
    // still shows the field, because there the webhook is the only transport.
    // Without this, hiding the field unconditionally would also pass.
    await stubPublicConfig(page, false);
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-install")).toHaveCount(0);
    await expect(page.getByLabel(/webhook url/i)).toHaveCount(1);
  });

  test("install CTA mints an org-scoped install URL for this channel", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubPublicConfig(page, true);
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

    await stubPublicConfig(page, true);
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
            users: [],
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

  // The production-like instance: Discord is "enabled", login works, and the
  // bot has neither a token nor an application public key. Offering the button
  // here is the bug — it is a round trip to discord.com that dead-ends.
  test("half-configured instance offers no install button at all", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubPublicConfig(page, false);
    await stubIntegration(page, LEGACY_UID, { webhook_url: "" });

    let installUrlCalled = false;
    await page.route(
      "**/api/v1/orgs/test/integrations/discord/install-url",
      async (route) => {
        installUrlCalled = true;
        await route.fulfill({ status: 404, body: "" });
      },
    );

    await page.goto(`orgs/test/integrations/${LEGACY_UID}`);
    await page.waitForLoadState("networkidle");

    // The panel still explains itself and still offers the webhook, which is
    // the only Discord delivery this instance can actually perform.
    await expect(page.getByTestId("discord-not-connected")).toBeVisible();
    await expect(
      page.getByTestId("discord-not-connected"),
    ).toContainText(/does not have the Discord bot configured/i);

    await expect(page.getByTestId("discord-install")).toHaveCount(0);
    expect(installUrlCalled).toBe(false);
  });

  // §4 of spec 2026-09-19-05: the DM destination tab.
  test("the DM tab lists only identity-resolved members", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubPublicConfig(page, true);
    await stubIntegration(page, BOT_UID, {
      guild_id: "G-ACME",
      guild_name: "acme",
      channel_id: "C-ALERTS",
      channel_name: "alerts",
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
            channels: [{ id: "C-ALERTS", name: "alerts", type: 0 }],
            // Only Alice. Bob is an org member with nothing linking him to
            // Discord, and the backend leaves him out — the list is what the
            // SENDER can address, never the guild member list.
            users: [{ id: "SNOW-ALICE", name: "Alice", userUid: "user-alice" }],
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${BOT_UID}`);
    await page.waitForLoadState("networkidle");

    // The channel tab is the one that opens for a channel destination.
    await expect(page.getByTestId("discord-channel-combobox")).toBeVisible();
    await expect(page.getByTestId("discord-user-combobox")).toHaveCount(0);

    await page.getByTestId("discord-tab-dm").click();

    const userCombobox = page.getByTestId("discord-user-combobox");
    await expect(userCombobox).toBeVisible();
    await expect(page.getByTestId("discord-channel-combobox")).toHaveCount(0);

    await userCombobox.click();
    await expect(page.getByTestId("discord-user-option-SNOW-ALICE")).toBeVisible();
    await expect(page.getByTestId("discord-user-option-SNOW-BOB")).toHaveCount(0);
  });

  test("picking a member opens the DM and stores dm_user_id", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubPublicConfig(page, true);

    const state = { lastPatch: null as Record<string, unknown> | null, dmCalls: 0 };

    await page.route(
      `**/api/v1/orgs/test/integrations/${BOT_UID}`,
      async (route) => {
        const method = route.request().method();

        if (method === "PATCH") {
          state.lastPatch = route.request().postDataJSON() as Record<
            string,
            unknown
          >;
          await route.fulfill({ status: 200, contentType: "application/json", body: "{}" });

          return;
        }

        if (method !== "GET") {
          await route.continue();

          return;
        }

        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: BOT_UID,
            type: "discord",
            name: "acme discord",
            enabled: true,
            isDefault: false,
            settings: {
              guild_id: "G-ACME",
              guild_name: "acme",
              channel_id: "C-ALERTS",
              channel_name: "alerts",
            },
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          }),
        });
      },
    );

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
            channels: [{ id: "C-ALERTS", name: "alerts", type: 0 }],
            users: [{ id: "SNOW-ALICE", name: "Alice", userUid: "user-alice" }],
          }),
        });
      },
    );

    // The DM is opened AT PICK TIME, server-side, so an admin finds out now
    // whether Discord will carry it rather than during the first incident.
    await page.route(
      `**/api/v1/orgs/test/channels/${BOT_UID}/discord/dm`,
      async (route) => {
        expect(route.request().method()).toBe("POST");
        expect(
          (route.request().postDataJSON() as { userId?: string }).userId,
        ).toBe("SNOW-ALICE");
        state.dmCalls += 1;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            channelId: "DM-ALICE",
            userId: "SNOW-ALICE",
            name: "Alice",
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${BOT_UID}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("discord-tab-dm").click();
    await page.getByTestId("discord-user-combobox").click();
    await page.getByTestId("discord-user-option-SNOW-ALICE").click();

    await expect.poll(() => state.dmCalls).toBe(1);
    await expect(page.getByTestId("discord-user-combobox")).toContainText("Alice");

    await page.getByRole("button", { name: /save|enregistrer/i }).click();

    // channel_id becomes the DM CHANNEL id, and dm_user_id names who it is —
    // which is how the sender tells a DM apart from a guild channel and skips
    // every thread operation for it.
    const settings = () =>
      (state.lastPatch?.settings as Record<string, unknown> | undefined) ?? {};
    await expect.poll(() => settings().dm_user_id).toBe("SNOW-ALICE");
    await expect.poll(() => settings().channel_id).toBe("DM-ALICE");
  });

  // The resolved open question: mentioning is meaningless in a DM, so the switch
  // is not offered there rather than offered and silently ignored.
  test("the DM tab offers no mention-on-call switch", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubPublicConfig(page, true);
    await stubIntegration(page, BOT_UID, {
      guild_id: "G-ACME",
      guild_name: "acme",
      channel_id: "DM-ALICE",
      dm_user_id: "SNOW-ALICE",
      mention_on_call: true,
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
            channels: [{ id: "C-ALERTS", name: "alerts", type: 0 }],
            users: [{ id: "SNOW-ALICE", name: "Alice", userUid: "user-alice" }],
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${BOT_UID}`);
    await page.waitForLoadState("networkidle");

    // A stored dm_user_id opens the DM tab, so the destination is never
    // misreported as a channel one.
    await expect(page.getByTestId("discord-user-combobox")).toBeVisible();
    await expect(page.getByTestId("discord-mention-on-call")).toHaveCount(0);

    // POSITIVE CONTROL: switching back to the channel tab brings it back, so the
    // assertion above is about the DM branch and not about a missing switch.
    await page.getByTestId("discord-tab-channel").click();
    await expect(page.getByTestId("discord-mention-on-call")).toBeVisible();
  });
});
