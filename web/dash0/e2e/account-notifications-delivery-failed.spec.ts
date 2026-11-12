import { test, expect, type Page } from "./fixtures";

/**
 * Spec 2026-11-12-07: a Slack DM contact that Slack rejected as unreachable
 * shows a warning badge in Account -> Notifications. The routes GET is stubbed;
 * the flagging itself is covered by the Go tests.
 */
async function stubRoutes(page: Page, failed: boolean) {
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
              uid: "route-slack",
              enabled: true,
              position: 0,
              createdAt: new Date().toISOString(),
              contact: {
                uid: "contact-slack",
                type: "slack_user",
                value: "U123",
                label: "Slack",
                ...(failed
                  ? {
                      deliveryFailedAt: new Date().toISOString(),
                      deliveryFailureCode: "user_not_found",
                    }
                  : {}),
              },
            },
          ],
        }),
      });
    },
  );
}

test.describe("Account Notifications — unreachable Slack contact", () => {
  test("shows a warning badge for a flagged contact", async ({
    authenticatedPage: page,
  }) => {
    await stubRoutes(page, true);
    await page.goto("orgs/test/account/notifications");

    const badge = page.getByTestId("contact-delivery-failed");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText(/could not reach this contact: user not found/i);
  });

  test("shows no badge for a healthy contact", async ({
    authenticatedPage: page,
  }) => {
    await stubRoutes(page, false);
    await page.goto("orgs/test/account/notifications");

    await expect(page.getByTestId("notification-route-row")).toBeVisible();
    await expect(page.getByTestId("contact-delivery-failed")).toHaveCount(0);
  });
});
