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

/**
 * Serves a public-config document with the Telegram capability on/off, and
 * optionally Sign in with Telegram on.
 */
async function stubPublicConfig(page: Page, telegramEnabled: boolean, loginEnabled = false) {
  await page.route("**/api/v1/config", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        posthog: { enabled: false },
        whatsapp: { enabled: false },
        telegram: telegramEnabled
          ? { enabled: true, botUsername: "solidping_test_bot", ...(loginEnabled ? { loginEnabled } : {}) }
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

const TEST_PASSWORD = "correct-horse-battery";

/**
 * Stubs /auth/me (hasPassword only), the routes list (empty) and the link
 * endpoint, answering it the way the server's re-authentication does when Sign
 * in with Telegram is on (spec 2026-11-13-03).
 */
async function stubReauthLink(
  page: Page,
  opts: { hasPassword: boolean; staleSignIn?: boolean },
) {
  const state = { linkBodies: [] as Array<{ currentPassword?: string }> };

  await page.route("**/api/v1/auth/me", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();

      return;
    }

    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, hasPassword: opts.hasPassword } });
  });

  await page.route("**/notification-routes**", async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      });

      return;
    }

    await route.continue();
  });

  await page.route("**/users/me/telegram/link", async (route) => {
    const body = route.request().postDataJSON() ?? {};
    state.linkBodies.push(body);

    if (opts.hasPassword && body.currentPassword !== TEST_PASSWORD) {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          code: "INVALID_CURRENT_PASSWORD",
          title: "Current password is incorrect",
        }),
      });

      return;
    }

    if (!opts.hasPassword && opts.staleSignIn) {
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({
          code: "REAUTHENTICATION_REQUIRED",
          title: "Sign in again to make this change",
        }),
      });

      return;
    }

    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        url: "https://t.me/solidping_test_bot?start=tok-123",
        expiresAt: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
      }),
    });
  });

  await page.context().route("https://t.me/**", (route) => route.abort());

  return state;
}

authTest.describe("Account Notifications — Telegram", () => {
  authTest(
    "hides the connect action when the instance capability is off",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, false);

      await page.goto("orgs/test/account/notifications");

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
      await expect(list.getByTestId("route-type-contact-telegram-1")).toHaveText(
        "Telegram",
        { timeout: 15000 },
      );
      await expect(page.getByTestId("telegram-connected-contact-telegram-1")).toBeVisible();
      await expect(pending).toHaveCount(0);
    },
  );

  authTest(
    "offers a working Test button on a connected contact",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      await page.route("**/notification-routes**", async (route) => {
        if (route.request().method() === "GET") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ data: [telegramRoute(true)] }),
          });

          return;
        }

        await route.continue();
      });

      let testPostReceived = false;
      await page.route("**/notification-routes/route-telegram-1/test", async (route) => {
        if (route.request().method() === "POST") {
          testPostReceived = true;
          await route.fulfill({ status: 204, body: "" });

          return;
        }

        await route.continue();
      });

      await page.goto("orgs/test/account/notifications");

      // The generic Test affordance every other ready method has.
      const testBtn = page.getByTestId("test-route-route-telegram-1");
      await expect(testBtn).toBeVisible();
      await testBtn.click();

      await expect.poll(() => testPostReceived).toBe(true);
      await expect(page.locator("[data-sonner-toaster]")).toContainText(
        "Test notification sent",
      );
    },
  );

  authTest(
    "hides the Test button while a contact awaits reconnection",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      await page.route("**/notification-routes**", async (route) => {
        if (route.request().method() === "GET") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            // verifiedAt cleared: the bot cannot reach this chat anymore, so a
            // test could only fail — reconnect is the one useful action.
            body: JSON.stringify({ data: [telegramRoute(false)] }),
          });

          return;
        }

        await route.continue();
      });

      await page.goto("orgs/test/account/notifications");

      await expect(page.getByTestId("telegram-reconnect-contact-telegram-1")).toBeVisible();
      await expect(page.getByTestId("test-route-route-telegram-1")).toHaveCount(0);
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

      await expect(page.getByTestId("notification-channel-notif-tg-1")).toHaveText(
        "Telegram",
      );
      // The reason is translated prose that names the fix, not a raw token.
      const reason = page.getByTestId("notification-reason-notif-tg-1");
      await expect(reason).toBeVisible();
      await expect(reason).toContainText(/blocked/i);
      await expect(reason).not.toContainText("telegram_bot_blocked");
    },
  );

  authTest(
    "asks for the password before connecting when Telegram sign-in is on",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true, true);
      const state = await stubReauthLink(page, { hasPassword: true });

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();
      await page.getByTestId("telegram-connect-button").click();

      // A connected chat signs in, so the password comes first: nothing is
      // requested before it is typed.
      await expect(page.getByTestId("telegram-link-reauth-dialog")).toBeVisible();
      expect(state.linkBodies).toHaveLength(0);

      // A wrong password is refused in the dialog, and the member stays signed in.
      await page.getByTestId("telegram-link-current-password").fill("not-the-password");
      await page.getByTestId("telegram-link-reauth-continue").click();
      await expect(page.getByTestId("telegram-link-reauth-error")).toBeVisible();
      await expect(page).toHaveURL(/account\/notifications/);

      await page.getByTestId("telegram-link-current-password").fill(TEST_PASSWORD);
      await page.getByTestId("telegram-link-reauth-continue").click();

      await expect(page.getByTestId("telegram-link-reauth-dialog")).toHaveCount(0);
      await expect(page.getByTestId("telegram-connect-pending")).toBeVisible();
      expect(state.linkBodies).toHaveLength(2);
      expect(state.linkBodies[1]).toMatchObject({ currentPassword: TEST_PASSWORD });
    },
  );

  authTest(
    "connects without a password when Telegram sign-in is off",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true, false);
      const state = await stubReauthLink(page, { hasPassword: false });

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();
      await page.getByTestId("telegram-connect-button").click();

      await expect(page.getByTestId("telegram-connect-pending")).toBeVisible();
      await expect(page.getByTestId("telegram-link-reauth-dialog")).toHaveCount(0);
      expect(state.linkBodies).toHaveLength(1);
      expect(state.linkBodies[0].currentPassword).toBeUndefined();
    },
  );

  authTest(
    "a passwordless account with a stale sign-in is told to sign in again",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true, true);
      const state = await stubReauthLink(page, { hasPassword: false, staleSignIn: true });

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();
      await page.getByTestId("telegram-connect-button").click();

      await expect(page.getByTestId("reauth-sign-in-required")).toBeVisible();
      await expect(page.getByTestId("telegram-connect-pending")).toHaveCount(0);
      expect(state.linkBodies).toHaveLength(1);
    },
  );
});
