import type { Page, Route } from "@playwright/test";
import { test, expect } from "./fixtures";

// The two-way Microsoft Teams bot (`msteams-bot`) is set up from the
// integration edit page: the panel states the public-HTTPS requirement,
// offers the generated app package, takes the tenant ID that links this org
// to a Microsoft 365 tenant, and lists the conversation references the
// backend captured when the bot was added to Teams channels.
//
// Everything Microsoft-side is stubbed: an install needs an Azure Bot
// registration, which no test environment here can supply.

const CHANNEL_UID = "33333333-3333-3333-3333-333333333333";
const TENANT_ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

interface StubOptions {
  tenantId?: string;
  destinations?: Array<Record<string, unknown>>;
  uninstalled?: boolean;
  enabled?: boolean;
}

async function stubTeamsBotConnection(page: Page, opts: StubOptions = {}) {
  const tenantId = opts.tenantId ?? TENANT_ID;
  const destinations = opts.destinations ?? [];

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}`,
    async (route: Route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid: CHANNEL_UID,
          type: "msteams-bot",
          name: "Microsoft Teams",
          enabled: true,
          isDefault: false,
          settings: {
            tenant_id: tenantId,
            channel_id: destinations[0]?.id ?? "",
            uninstalled_at: opts.uninstalled ? new Date().toISOString() : "",
          },
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        }),
      });
    },
  );

  await page.route("**/api/v1/integrations/msteams/status", async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        enabled: opts.enabled ?? true,
        configured: true,
        appId: "11111111-2222-3333-4444-555555555555",
        messagingEndpoint:
          "https://monitor.example.com/api/v1/integrations/msteams/messages",
        installedTenants: 1,
      }),
    });
  });

  await page.route(
    `**/api/v1/orgs/test/channels/${CHANNEL_UID}/msteams/destinations`,
    async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          destinations,
          tenantId,
          connected: !opts.uninstalled,
          uninstalled: Boolean(opts.uninstalled),
        }),
      });
    },
  );
}

test.describe("Microsoft Teams bot setup page", () => {
  test("lists captured Teams channels as destinations", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubTeamsBotConnection(page, {
      destinations: [
        {
          id: "19:channel-a",
          name: "alerts",
          team_id: "19:team",
          team_name: "Ops",
          type: "channel",
        },
        {
          id: "19:channel-b",
          name: "incidents",
          team_id: "19:team",
          team_name: "Ops",
          type: "channel",
        },
      ],
    });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("msteams-bot-panel")).toBeVisible();

    // The public-HTTPS requirement is stated up front — it is the single most
    // common self-hosted misconfiguration and Bot Framework has no
    // outbound-dialing fallback.
    await expect(page.getByTestId("msteams-bot-endpoint")).toContainText(
      "/api/v1/integrations/msteams/messages",
    );

    // The generated, instance-filled app package is downloadable.
    await expect(page.getByTestId("msteams-bot-manifest")).toBeVisible();

    // The tenant linkage field is pre-filled from the stored settings.
    await expect(page.getByTestId("msteams-bot-tenant")).toHaveValue(TENANT_ID);

    // Both captured conversations are offered as destinations.
    const list = page.getByTestId("msteams-bot-destinations");
    await expect(list).toBeVisible();
    await expect(
      page.getByTestId("msteams-bot-destination-19:channel-a"),
    ).toContainText("alerts");
    await expect(
      page.getByTestId("msteams-bot-destination-19:channel-b"),
    ).toContainText("incidents");
  });

  test("explains the empty state when the bot is in no channel yet", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubTeamsBotConnection(page, { destinations: [] });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("msteams-bot-empty")).toBeVisible();
    await expect(page.getByTestId("msteams-bot-destinations")).toHaveCount(0);
  });

  test("surfaces an uninstalled tenant instead of an empty picker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubTeamsBotConnection(page, { destinations: [], uninstalled: true });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("msteams-bot-uninstalled")).toBeVisible();
  });

  test("warns when the bot is disabled on the server", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubTeamsBotConnection(page, { destinations: [], enabled: false });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("msteams-bot-disabled")).toBeVisible();
  });

  test("the new-integration picker offers the Teams bot type", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("pick-msteams-bot")).toBeVisible();
  });
});
