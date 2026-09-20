import { test, expect } from "./fixtures";

// §5 of spec 2026-09-19-05, mirroring slack-comment-ingestion: the Discord
// integration edit page exposes a "Capture every thread reply as a comment"
// toggle. OFF is the default AND the meaning of an absent key — a legacy row
// must not over-capture, because guessing the other way writes private triage
// chatter into a permanent, fanned-out incident timeline.
//
// Every backend call is stubbed, so this asserts the UI contract (default-off,
// and what each state PATCHes) rather than a live guild.

const CHANNEL_UID = "66666666-6666-6666-6666-666666666666";

type PatchBody = { settings?: Record<string, unknown> };

async function stubDiscordIntegration(
  page: import("./fixtures").Page,
  commentIngestion?: string,
) {
  const state = { lastPatch: null as PatchBody | null };

  const settings: Record<string, unknown> = {
    guild_id: "G-ACME",
    guild_name: "acme",
    channel_id: "C-ALERTS",
    channel_name: "alerts",
    mention_on_call: true,
  };

  if (commentIngestion !== undefined) {
    settings.comment_ingestion = commentIngestion;
  }

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

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}`,
    async (route) => {
      const method = route.request().method();

      if (method === "PATCH") {
        state.lastPatch = route.request().postDataJSON() as PatchBody;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: CHANNEL_UID,
            type: "discord",
            name: "acme discord",
            enabled: true,
            isDefault: false,
            settings: { ...settings, ...(state.lastPatch?.settings ?? {}) },
          }),
        });

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
          uid: CHANNEL_UID,
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

  await page.route(
    `**/api/v1/orgs/test/channels/${CHANNEL_UID}/discord/destinations`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          guildId: "G-ACME",
          guildName: "acme",
          connected: true,
          channels: [{ id: "C-ALERTS", name: "alerts", type: 0 }],
          users: [],
        }),
      });
    },
  );

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}/identities*`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      });
    },
  );

  return state;
}

test.describe("Discord comment ingestion toggle", () => {
  test("is off for an integration with no stored preference", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubDiscordIntegration(page);

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const toggle = page.getByTestId("discord-comment-ingestion");
    await expect(toggle).toBeVisible();
    // An absent key means `explicit`. The toggle has to SAY that, or an operator
    // reads "off" off the switch while the backend does something else.
    await expect(toggle).toHaveAttribute("data-state", "unchecked");
  });

  test("reflects the stored 'all' preference", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubDiscordIntegration(page, "all");

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByTestId("discord-comment-ingestion"),
    ).toHaveAttribute("data-state", "checked");
  });

  test("turning it on saves comment_ingestion=all", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubDiscordIntegration(page, "explicit");

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("discord-comment-ingestion").click();
    await expect(
      page.getByTestId("discord-comment-ingestion"),
    ).toHaveAttribute("data-state", "checked");

    await page.getByRole("button", { name: /save|enregistrer/i }).click();

    // The exact string the backend reads
    // (models.DiscordCommentIngestionAll) — a UI that saved "true" or "ALL"
    // would pass a looser assertion and silently ingest nothing.
    await expect
      .poll(() => state.lastPatch?.settings?.comment_ingestion)
      .toBe("all");
  });
});
