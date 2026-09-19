import { test, expect, type Page } from "./fixtures";

/**
 * §2 of spec 2026-09-19-02: Account → Notifications tells a member how they
 * appear in the org's Slack CHANNEL alerts.
 *
 * The routes GET is stubbed so all three states are reachable deterministically
 * — the test org has no Slack workspace, and whether a real one resolves a
 * handle depends on data this suite must not own. The contract under test is
 * the UI's, not the resolver's (that is covered by the Go tests).
 */

type SlackMention = {
  linked: boolean;
  externalId?: string;
  workspace?: string;
};

async function stubRoutes(page: Page, slackMention?: SlackMention) {
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
          data: [
            {
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
            },
          ],
          ...(slackMention ? { slackMention } : {}),
        }),
      });
    },
  );
}

test.describe("Account Notifications — Slack channel mention", () => {
  test("names the handle a member will be pinged with", async ({
    authenticatedPage: page,
  }) => {
    await stubRoutes(page, {
      linked: true,
      externalId: "U-ADAM",
      workspace: "Acme",
    });

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    const status = page.getByTestId("slack-mention-status");
    await expect(status).toBeVisible();
    await expect(status).toContainText("<@U-ADAM>");
  });

  test("says so when nothing links the member to the workspace", async ({
    authenticatedPage: page,
  }) => {
    await stubRoutes(page, { linked: false, workspace: "Acme" });

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    const status = page.getByTestId("slack-mention-status");
    await expect(status).toBeVisible();
    await expect(status).toContainText(/Not linked/i);
    await expect(status).not.toContainText("<@");
  });

  test("stays silent when the org has no Slack workspace at all", async ({
    authenticatedPage: page,
  }) => {
    // Negative control: with no slackMention in the payload there is nothing
    // to say about channel mentions, so the line must not appear — the Slack
    // row itself still does.
    await stubRoutes(page);

    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("slack-connect-row")).toBeVisible();
    await expect(page.getByTestId("slack-mention-status")).toHaveCount(0);
  });
});
