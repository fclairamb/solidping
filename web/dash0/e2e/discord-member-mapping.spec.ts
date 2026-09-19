import { test, expect } from "./fixtures";

// §3 of spec 2026-09-19-05: a bot-installed Discord integration exposes the same
// "Member mapping" section Slack has — with one structural difference the Slack
// suite cannot assert, because Slack can never be in this state.
//
// Discord has NO look-up-by-email endpoint for bots and no readable member list
// without the privileged GUILD_MEMBERS intent, so there is nothing for an admin
// to pick FROM. The panel therefore renders no picker at all, and its empty state
// has to name the affordance that does exist: the member connects their own
// account. Every backend call is stubbed, so this asserts the UI contract rather
// than a live guild.

const CHANNEL_UID = "55555555-5555-5555-5555-555555555555";

const IDENTITIES = {
  data: [
    {
      userUid: "user-alice",
      email: "alice@acme.test",
      name: "Alice",
      status: "matched",
      externalId: "SNOW-ALICE",
      displayName: "Alice",
      source: "auto",
    },
    {
      userUid: "user-bob",
      email: "bob@acme.test",
      name: "Bob",
      status: "notFound",
    },
  ],
};

/** Nobody in the org has linked Discord — the state the empty-state copy is for. */
const NOBODY_LINKED = {
  data: [
    {
      userUid: "user-bob",
      email: "bob@acme.test",
      name: "Bob",
      status: "notFound",
    },
  ],
};

async function stubPublicConfig(page: import("./fixtures").Page) {
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
        discord: { botEnabled: true },
      }),
    });
  });
}

async function stubDiscordIntegration(
  page: import("./fixtures").Page,
  opts: {
    /**
     * "ok" (default): destinations 200. "error": destinations 409, the shape the
     * deployed API sends when the bot token cannot be resolved — the mapping card
     * must still show who is mapped.
     */
    destinationsMode?: "ok" | "error";
    /** Whether anybody in the org has a resolved Discord identity. */
    anybodyLinked?: boolean;
  } = {},
) {
  const destinationsMode = opts.destinationsMode ?? "ok";
  const anybodyLinked = opts.anybodyLinked ?? true;
  const state = { syncCalls: 0, deleteCalls: 0 };

  await stubPublicConfig(page);

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}`,
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();

        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid: CHANNEL_UID,
          type: "discord",
          name: "acme discord",
          enabled: true,
          isDefault: false,
          settings: {
            guild_id: "G-ACME",
            guild_name: "acme",
            channel_id: "C-ALERTS",
            channel_name: "alerts",
            mention_on_call: false,
          },
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        }),
      });
    },
  );

  await page.route(
    `**/api/v1/orgs/test/channels/${CHANNEL_UID}/discord/destinations`,
    async (route) => {
      if (destinationsMode === "error") {
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            code: "CHANNEL_NOT_CONNECTED",
            title: "Discord server is not connected — install the SolidPing bot",
          }),
        });

        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          guildId: "G-ACME",
          guildName: "acme",
          connected: true,
          channels: [{ id: "C-ALERTS", name: "alerts", type: 0 }],
          users: anybodyLinked
            ? [{ id: "SNOW-ALICE", name: "Alice", userUid: "user-alice" }]
            : [],
        }),
      });
    },
  );

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}/identities/*`,
    async (route) => {
      if (route.request().method() === "DELETE") {
        state.deleteCalls += 1;
        await route.fulfill({ status: 204, body: "" });

        return;
      }

      await route.continue();
    },
  );

  // Playwright gives precedence to the LAST matching route, so the broad
  // `/identities/*` pattern above must be registered before these two.
  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}/identities`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(anybodyLinked ? IDENTITIES : NOBODY_LINKED),
      });
    },
  );

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}/identities/sync`,
    async (route) => {
      state.syncCalls += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...(anybodyLinked ? IDENTITIES : NOBODY_LINKED),
          matchedCount: anybodyLinked ? 1 : 0,
          notFoundCount: 1,
          ambiguousCount: 0,
        }),
      });
    },
  );

  return state;
}

test.describe("Discord member mapping", () => {
  test("shows matched and not-found members with a re-sync action", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubDiscordIntegration(page);

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slack-member-mapping")).toBeVisible();

    const aliceRow = page.getByTestId("slack-mapping-row-alice@acme.test");
    const bobRow = page.getByTestId("slack-mapping-row-bob@acme.test");
    await expect(aliceRow).toBeVisible();
    await expect(bobRow).toBeVisible();
    await expect(
      aliceRow.getByTestId("slack-mapping-status-matched"),
    ).toBeVisible();
    await expect(
      bobRow.getByTestId("slack-mapping-status-notfound"),
    ).toBeVisible();

    // Re-sync picks up whoever connected Discord since the last run. It is the
    // ONLY admin-side action here, which is why it matters that it works.
    await page.getByTestId("slack-mapping-sync").click();
    await expect.poll(() => state.syncCalls).toBe(1);
  });

  // The assertion the Slack suite cannot have: Slack ALWAYS has a picker,
  // Discord never does.
  test("renders no picker at all for the discord variant", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubDiscordIntegration(page);

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const aliceRow = page.getByTestId("slack-mapping-row-alice@acme.test");
    await expect(aliceRow).toBeVisible();

    // Not merely hidden — absent. A CSS-hidden combobox would still be
    // focusable by keyboard and would still be a lie about what an admin can do.
    await expect(
      aliceRow.getByTestId("slack-user-combobox"),
    ).toHaveCount(0);
    await expect(page.getByTestId("slack-user-combobox")).toHaveCount(0);

    // The help text says how the matching actually happens.
    await expect(
      page.getByText(/Members who signed in with Discord are matched/i),
    ).toBeVisible();
  });

  test("clearing a mapping is the red trash action", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubDiscordIntegration(page);

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const clear = page.getByTestId("slack-mapping-clear-alice@acme.test");
    await expect(clear).toBeEnabled();
    // Delete is always the red trash bin.
    await expect(clear.locator("svg")).toHaveClass(/text-destructive/);

    // An unmapped member has nothing to clear.
    await expect(
      page.getByTestId("slack-mapping-clear-bob@acme.test"),
    ).toBeDisabled();

    await clear.click();
    await expect.poll(() => state.deleteCalls).toBe(1);
  });

  test("the empty state names the member-side affordance, not an admin one", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubDiscordIntegration(page, { anybodyLinked: false });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const empty = page.getByTestId("discord-mapping-nobody-linked");
    await expect(empty).toBeVisible();
    // The point of the copy: the MEMBER acts, from their own account page.
    await expect(empty).toContainText(/Account/i);
    await expect(empty).toContainText(/Notifications/i);
    await expect(empty).toContainText(/re-sync/i);
  });

  test("mention-on-call switch reflects the stored setting", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubDiscordIntegration(page);

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const toggle = page.getByTestId("discord-mention-on-call");
    await expect(toggle).toBeVisible();
    // Stored false must render OFF — the "existing integrations are unchanged"
    // promise, visibly.
    await expect(toggle).toHaveAttribute("data-state", "unchecked");

    await toggle.click();
    await expect(toggle).toHaveAttribute("data-state", "checked");
  });

  test("a destinations 409 still shows who is mapped", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubDiscordIntegration(page, { destinationsMode: "error" });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    // The destinations failure takes out the channel picker…
    await expect(page.getByTestId("discord-channel-combobox")).toHaveCount(0);

    // …but not the mapping card. Discord's mapping never needed the
    // destinations call in the first place, which is precisely why it survives.
    await expect(page.getByTestId("slack-member-mapping")).toBeVisible();
    await expect(
      page
        .getByTestId("slack-mapping-row-alice@acme.test")
        .getByTestId("slack-mapping-status-matched"),
    ).toBeVisible();
    await expect(
      page.getByTestId("slack-mapping-clear-alice@acme.test"),
    ).toBeEnabled();
  });
});
