import { test, expect } from "./fixtures";

// Spec 2026-08-15-08: the Slack integration edit page exposes a
// "Capture every thread reply as a comment" toggle. OFF is the default and
// means `comment_ingestion: "explicit"` — only `/comment` creates an incident
// comment. Every backend call is stubbed, so this asserts the UI contract
// (default-off, and what each state PATCHes) rather than a live workspace.

const CHANNEL_UID = "44444444-4444-4444-4444-444444444444";

type PatchBody = { settings?: Record<string, unknown> };

/**
 * Stubs the integration GET + PATCH and the destinations picker.
 * `commentIngestion` is omitted from settings entirely when undefined, which
 * is exactly the shape of an integration stored before the field existed.
 */
async function stubSlackIntegration(
  page: import("./fixtures").Page,
  commentIngestion?: string,
) {
  const state = { lastPatch: null as PatchBody | null };

  const settings: Record<string, unknown> = {
    team_id: "T1",
    team_name: "Acme",
    channel_id: "C1",
    channel_name: "alerts",
    destination_type: "channel",
    mention_on_call: true,
  };

  if (commentIngestion !== undefined) {
    settings.comment_ingestion = commentIngestion;
  }

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
            type: "slack",
            name: "Acme Slack",
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
          type: "slack",
          name: "Acme Slack",
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
    `**/api/v1/orgs/test/channels/${CHANNEL_UID}/slack/destinations`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          channels: [
            { id: "C1", name: "alerts", isPrivate: false, isMember: true },
          ],
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

test.describe("Slack comment ingestion toggle", () => {
  test("is off for an integration with no stored preference", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubSlackIntegration(page);

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const toggle = page.getByTestId("slack-comment-ingestion");
    await expect(toggle).toBeVisible();
    // The whole point of the change: a legacy row stops over-capturing without
    // any backfill.
    await expect(toggle).toHaveAttribute("data-state", "unchecked");
  });

  test("reflects the stored 'all' preference", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubSlackIntegration(page, "all");

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slack-comment-ingestion")).toHaveAttribute(
      "data-state",
      "checked",
    );
  });

  test("turning it on saves comment_ingestion=all", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubSlackIntegration(page, "explicit");

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("slack-comment-ingestion").click();
    await expect(page.getByTestId("slack-comment-ingestion")).toHaveAttribute(
      "data-state",
      "checked",
    );

    await page.getByRole("button", { name: /save|enregistrer/i }).click();

    await expect
      .poll(() => state.lastPatch?.settings?.comment_ingestion)
      .toBe("all");
  });
});
