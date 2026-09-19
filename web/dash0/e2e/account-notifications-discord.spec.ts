import { test, expect, type Page } from "./fixtures";

/**
 * §1–3 of spec 2026-09-19-05: Account → Notifications is where a member binds
 * their own Discord account, and where they find out how they appear in the
 * org's Discord CHANNEL alerts.
 *
 * Two connect paths and deliberately no third. There is no field to type a
 * Discord id into, because a Discord user id is PUBLIC — the backend rejects a
 * typed one outright — so either a Discord sign-in is already on file (one click)
 * or the member completes the OAuth link round trip (one redirect).
 *
 * Everything is stubbed: whether a real Discord account resolves depends on data
 * this suite must not own, and the resolver itself is covered by the Go tests.
 * The contract under test is the UI's.
 */

type DiscordMention = {
  linked: boolean;
  externalId?: string;
  guild?: string;
};

type StubOptions = {
  botEnabled?: boolean;
  /** A Discord sign-in is on file → the one-click Connect path. */
  suggestion?: boolean;
  /** A verified `discord` contact already exists. */
  connectedContact?: boolean;
  mention?: DiscordMention;
};

const EMAIL_ROUTE = {
  uid: "route-email",
  enabled: true,
  position: 0,
  createdAt: new Date().toISOString(),
  contact: {
    uid: "contact-email",
    type: "email",
    value: "test@test.com",
    label: "Email",
    verifiedAt: new Date().toISOString(),
  },
};

const DISCORD_ROUTE = {
  uid: "route-discord",
  enabled: true,
  position: 1,
  createdAt: new Date().toISOString(),
  contact: {
    uid: "contact-discord",
    type: "discord",
    value: "111222333444555666",
    label: "Discord",
    verifiedAt: new Date().toISOString(),
  },
};

async function stubAccount(page: Page, opts: StubOptions = {}) {
  const botEnabled = opts.botEnabled ?? true;
  const state = { connectCalls: 0, linkCalls: 0, testCalls: 0 };

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

  await page.route(
    "**/api/v1/orgs/test/users/me/notification-routes",
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();

        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: opts.connectedContact
            ? [EMAIL_ROUTE, DISCORD_ROUTE]
            : [EMAIL_ROUTE],
          ...(opts.suggestion
            ? { discordSuggestion: { discordUserId: "111222333444555666" } }
            : {}),
          ...(opts.mention ? { discordMention: opts.mention } : {}),
        }),
      });
    },
  );

  await page.route(
    "**/api/v1/orgs/test/users/me/discord/connect",
    async (route) => {
      state.connectCalls += 1;
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({ ...DISCORD_ROUTE }),
      });
    },
  );

  await page.route(
    "**/api/v1/orgs/test/users/me/discord/link-start",
    async (route) => {
      state.linkCalls += 1;
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          url: "https://discord.com/oauth2/authorize?client_id=fake&state=link%3Atok",
          expiresAt: new Date(Date.now() + 900_000).toISOString(),
        }),
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

  return state;
}

test.describe("Account Notifications — Discord", () => {
  test("a Discord sign-in on file gives a one-click connect", async ({
    authenticatedPage: page,
  }) => {
    const state = await stubAccount(page, { suggestion: true });

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-connect-row")).toBeVisible();

    const connect = page.getByTestId("discord-connect-button");
    await expect(connect).toBeVisible();
    await expect(connect).toHaveText(/connect discord/i);
    // No link-mode button when the cheap path is available.
    await expect(page.getByTestId("discord-link-button")).toHaveCount(0);

    await connect.click();
    await expect.poll(() => state.connectCalls).toBe(1);
  });

  test("with no sign-in on file the entry point is the OAuth link round trip", async ({
    authenticatedPage: page,
  }) => {
    const state = await stubAccount(page);

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    // Crucially there is NO input to type a Discord id into. A Discord user id is
    // public, so a typed one would let anybody page a stranger — the backend
    // rejects it, and the UI must not invite it in the first place.
    await expect(page.getByTestId("discord-connect-button")).toHaveCount(0);

    const link = page.getByTestId("discord-link-button");
    await expect(link).toBeVisible();
    await expect(link).toHaveText(/link discord/i);

    await link.click();
    await expect.poll(() => state.linkCalls).toBe(1);
    // The browser leaves for Discord: nothing is created until Discord sends the
    // member back with a code.
    await page.waitForURL(/discord\.com\/oauth2\/authorize/);
  });

  test("returning with ?discord_linked=1 creates the contact and cleans the URL", async ({
    authenticatedPage: page,
  }) => {
    const state = await stubAccount(page);

    // This is the return leg of the OAuth round trip. The public callback wrote
    // the user_providers row and nothing else — creating the contact is an
    // AUTHENTICATED call made from here, which is why the callback is allowed to
    // write so little.
    await page.goto("orgs/test/account/notifications?discord_linked=1");
    await page.waitForLoadState("networkidle");

    await expect.poll(() => state.connectCalls).toBe(1);

    // The marker is stripped, so a refresh cannot replay it.
    await expect.poll(() => new URL(page.url()).search).not.toContain(
      "discord_linked",
    );
  });

  test("names the handle a member will be pinged with", async ({
    authenticatedPage: page,
  }) => {
    await stubAccount(page, {
      connectedContact: true,
      mention: { linked: true, externalId: "SNOW-ADAM", guild: "acme" },
    });

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    const status = page.getByTestId("discord-mention-status");
    await expect(status).toBeVisible();
    await expect(status).toContainText("<@SNOW-ADAM>");
  });

  test("says so when nothing links the member to the server", async ({
    authenticatedPage: page,
  }) => {
    await stubAccount(page, { mention: { linked: false, guild: "acme" } });

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    const status = page.getByTestId("discord-mention-status");
    await expect(status).toBeVisible();
    await expect(status).toContainText(/Nothing links you to this server/i);
    await expect(status).not.toContainText("<@");
  });

  test("stays silent about mentions when the org has no Discord bot integration", async ({
    authenticatedPage: page,
  }) => {
    // Negative control: with no discordMention in the payload there is nothing to
    // say about channel mentions, so the line must not appear — the row itself
    // still does, because a DM needs no org integration at all.
    await stubAccount(page);

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("discord-connect-row")).toBeVisible();
    await expect(page.getByTestId("discord-mention-status")).toHaveCount(0);
  });

  test("the row is absent entirely on an instance with no Discord bot", async ({
    authenticatedPage: page,
  }) => {
    // A DM goes through the INSTANCE bot, so an instance without one cannot
    // deliver a Discord DM at all. Offering the row would be offering a contact
    // that can never be paged.
    await stubAccount(page, { botEnabled: false, suggestion: true });

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slack-connect-row")).toBeVisible();
    await expect(page.getByTestId("discord-connect-row")).toHaveCount(0);
  });

  test("the Test button surfaces Discord's 50007 remedy verbatim", async ({
    authenticatedPage: page,
  }) => {
    await stubAccount(page, { connectedContact: true });

    // 422 is what the handler answers for a provider refusal, carrying the
    // service error's own wording. A generic "test failed" would leave the member
    // with nothing to act on, which is the whole reason this message exists.
    await page.route(
      "**/api/v1/orgs/test/users/me/notification-routes/route-discord/test",
      async (route) => {
        await route.fulfill({
          status: 422,
          contentType: "application/json",
          body: JSON.stringify({
            code: "VALIDATION_ERROR",
            title:
              "test failed: Discord refused the DM — open your DMs for server members, or join the server the bot is in",
          }),
        });
      },
    );

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("notification-routes-list");
    await expect(row).toBeVisible();

    await page
      .getByTestId("test-route-route-discord")
      .click();

    await expect(
      page.getByText(/open your DMs for server members/i).first(),
    ).toBeVisible();
  });
});
