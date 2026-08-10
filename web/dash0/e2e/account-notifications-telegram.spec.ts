import { type Page } from "@playwright/test";
import { test as authTest, expect } from "./fixtures";

/**
 * E2E for the Telegram connect flow.
 *
 * Telegram is an *instance*-level capability: the dashboard learns whether it
 * is available from the unauthenticated `GET /api/v1/config` document. The
 * dev/test server has no bot configured, so these tests stub that one endpoint
 * to drive both sides of the flag, and stub the link/routes endpoints so no
 * request ever reaches api.telegram.org. The server-side equivalent for a live
 * run is `SP_TELEGRAM_BASE_URL` pointed at a fake Bot API.
 *
 * The contact itself is never created from the browser — that is the point of
 * the design. Here the "user pressed Start" step is simulated by flipping what
 * the mocked routes list returns, which is exactly what the real webhook would
 * cause the next poll to see.
 */

/** Serves a public-config document with the Telegram capability on/off. */
async function stubPublicConfig(page: Page, telegramEnabled: boolean) {
  await page.route("**/api/v1/config", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        posthog: { enabled: false },
        whatsapp: { enabled: false },
        telegram: telegramEnabled
          ? { enabled: true, botUsername: "solidping_test_bot" }
          : { enabled: false },
      }),
    });
  });
}

/** One telegram route, verified or awaiting reconnection. */
function telegramRoute(verified: boolean) {
  return {
    uid: "route-telegram-1",
    enabled: true,
    position: 1,
    contact: {
      uid: "contact-telegram-1",
      type: "telegram",
      value: "987654321",
      label: "@flo",
      ...(verified ? { verifiedAt: new Date().toISOString() } : {}),
    },
    createdAt: new Date().toISOString(),
  };
}

authTest.describe("Account Notifications — Telegram", () => {
  authTest(
    "hides the connect action when the instance capability is off",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, false);

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();

      // The always-available options are there; Telegram must not be.
      await expect(page.getByTestId("add-contact-type-phone")).toBeVisible();
      await expect(page.getByTestId("telegram-connect-button")).toHaveCount(0);
    },
  );

  authTest(
    "offers the connect action when the instance capability is on",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();

      await expect(page.getByTestId("telegram-connect-button")).toBeVisible();
    },
  );

  authTest(
    "mints a link, waits with a countdown, and shows the contact once the webhook links it",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      // Flipped once the "user presses Start" — i.e. once the webhook has run.
      let linked = false;

      await page.route("**/notification-routes**", async (route) => {
        if (route.request().method() === "GET") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ data: linked ? [telegramRoute(true)] : [] }),
          });

          return;
        }

        await route.continue();
      });

      let linkRequests = 0;
      await page.route("**/users/me/telegram/link", async (route) => {
        if (route.request().method() === "POST") {
          linkRequests++;
          await route.fulfill({
            status: 201,
            contentType: "application/json",
            body: JSON.stringify({
              url: "https://t.me/solidping_test_bot?start=tok-123",
              expiresAt: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
            }),
          });

          return;
        }

        await route.continue();
      });

      // The connect link opens in a new tab; keep it from actually loading t.me.
      await page.context().route("https://t.me/**", (route) => route.abort());

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();
      await page.getByTestId("telegram-connect-button").click();

      await expect.poll(() => linkRequests).toBe(1);

      // Waiting state, with a live m:ss countdown.
      const pending = page.getByTestId("telegram-connect-pending");
      await expect(pending).toBeVisible();
      await expect(pending).toContainText(/\d+:\d{2}/);

      // The user presses Start in Telegram; the webhook creates the contact and
      // the poller picks it up without any further interaction here.
      linked = true;

      const list = page.getByTestId("notification-routes-list");
      await expect(list.getByText("Telegram", { exact: true })).toBeVisible({ timeout: 15000 });
      await expect(page.getByTestId("telegram-connected-contact-telegram-1")).toBeVisible();
      await expect(pending).toHaveCount(0);
    },
  );

  authTest(
    "shows 'reconnect needed' with a reconnect action for a contact the bot can no longer reach",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      await page.route("**/notification-routes**", async (route) => {
        if (route.request().method() === "GET") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            // verifiedAt cleared: this is what a blocked bot leaves behind.
            body: JSON.stringify({ data: [telegramRoute(false)] }),
          });

          return;
        }

        await route.continue();
      });

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await expect(page.getByTestId("telegram-reconnect-contact-telegram-1")).toBeVisible();
      // ...and NOT the generic verified badge.
      await expect(page.getByTestId("telegram-connected-contact-telegram-1")).toHaveCount(0);

      // The reconnect affordance is the same connect action, not a code dialog.
      await expect(page.getByTestId("telegram-connect-button").first()).toBeVisible();
      await expect(page.getByTestId("verify-phone-dialog")).toHaveCount(0);
    },
  );

  authTest(
    "renders the Telegram channel and a translated failure reason in the delivery history",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      // Scope the mock to the API path: the dashboard route is
      // `orgs/test/me/notifications`, so a looser pattern would also intercept
      // the HTML document navigation and serve it JSON.
      await page.route("**/api/v1/orgs/*/me/notifications**", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: [
              {
                uid: "notif-tg-1",
                incidentUid: "incident-1",
                eventType: "incident.escalated",
                source: "escalation_user",
                channelType: "telegram",
                status: "skipped",
                skipReason: "telegram_bot_blocked",
                createdAt: new Date().toISOString(),
              },
            ],
          }),
        });
      });

      await page.goto("orgs/test/me/notifications");
      await page.waitForLoadState("networkidle");

      await expect(page.getByText("Telegram", { exact: true }).first()).toBeVisible();
      // The reason is translated prose that names the fix, not a raw token.
      const reason = page.getByTestId("notification-reason-notif-tg-1");
      await expect(reason).toBeVisible();
      await expect(reason).toContainText(/blocked/i);
      await expect(reason).not.toContainText("telegram_bot_blocked");
    },
  );
});
