import { test, expect } from "./fixtures";

// Phase 1 UI of spec 2026-08-12-03: a connected Slack integration exposes a
// "Member mapping" section (matched / not-found buckets, re-sync, manual
// override, destructive clear) plus the "Mention the on-call person in alerts"
// switch. Every backend call is stubbed so the test asserts the UI contract
// rather than a live workspace.

const CHANNEL_UID = "33333333-3333-3333-3333-333333333333";

const IDENTITIES = {
  data: [
    {
      userUid: "user-alice",
      email: "alice@acme.test",
      name: "Alice",
      status: "matched",
      externalId: "U-ALICE",
      displayName: "Alice A",
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

/** Stubs the integration GET, the destinations picker and the identity APIs. */
async function stubSlackIntegration(
  page: import("./fixtures").Page,
  opts: { mentionOnCall: boolean },
) {
  const state = { syncCalls: 0, lastPut: null as unknown, deleteCalls: 0 };

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
          type: "slack",
          name: "Acme Slack",
          enabled: true,
          isDefault: false,
          settings: {
            team_id: "T1",
            team_name: "Acme",
            channel_id: "C1",
            channel_name: "alerts",
            destination_type: "channel",
            mention_on_call: opts.mentionOnCall,
          },
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
          users: [
            { id: "U-ALICE", name: "alice", realName: "Alice A" },
            { id: "U-BOB", name: "bob", realName: "Bob B" },
          ],
        }),
      });
    },
  );

  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}/identities/*`,
    async (route) => {
      const method = route.request().method();

      if (method === "PUT") {
        state.lastPut = route.request().postDataJSON();
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            userUid: "user-bob",
            email: "bob@acme.test",
            status: "matched",
            externalId: "U-BOB",
            source: "manual",
          }),
        });

        return;
      }

      if (method === "DELETE") {
        state.deleteCalls += 1;
        await route.fulfill({ status: 204, body: "" });

        return;
      }

      await route.continue();
    },
  );

  // Playwright gives precedence to the LAST matching route, so the broad
  // `/identities/*` pattern above must be registered before these two more
  // specific ones — otherwise it would swallow the sync POST.
  await page.route(
    `**/api/v1/orgs/test/integrations/${CHANNEL_UID}/identities`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(IDENTITIES),
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
          ...IDENTITIES,
          matchedCount: 1,
          notFoundCount: 1,
          ambiguousCount: 0,
        }),
      });
    },
  );

  return state;
}

test.describe("Slack member mapping", () => {
  test("shows matched and not-found members with a re-sync action", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubSlackIntegration(page, { mentionOnCall: true });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slack-member-mapping")).toBeVisible();

    // Both buckets are rendered, each with its own status badge.
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

    // Re-sync hits the admin sync endpoint.
    await page.getByTestId("slack-mapping-sync").click();
    await expect.poll(() => state.syncCalls).toBe(1);
  });

  test("manual override PUTs the picked workspace user", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubSlackIntegration(page, { mentionOnCall: true });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const bobRow = page.getByTestId("slack-mapping-row-bob@acme.test");
    await bobRow.getByTestId("slack-user-combobox").click();
    await page.getByTestId("slack-user-option-U-BOB").click();

    await expect
      .poll(() => (state.lastPut as { externalId?: string } | null)?.externalId)
      .toBe("U-BOB");
  });

  test("clearing a mapping uses the destructive trash action", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const state = await stubSlackIntegration(page, { mentionOnCall: true });

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

  test("mention-on-call switch reflects the stored setting", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubSlackIntegration(page, { mentionOnCall: false });

    await page.goto(`orgs/test/integrations/${CHANNEL_UID}`);
    await page.waitForLoadState("networkidle");

    const toggle = page.getByTestId("slack-mention-on-call");
    await expect(toggle).toBeVisible();
    // An existing integration whose settings say false must render OFF —
    // this is the "existing integrations are unchanged" promise, visibly.
    await expect(toggle).toHaveAttribute("data-state", "unchecked");

    await toggle.click();
    await expect(toggle).toHaveAttribute("data-state", "checked");
  });
});
