import { test, expect, type Page } from "@playwright/test";
import { test as authTest, API_BASE } from "./fixtures";

/**
 * E2E for the WhatsApp notification-contact flow.
 *
 * WhatsApp is an *instance*-level capability: the dashboard learns whether it
 * is available from the unauthenticated `GET /api/v1/config` document. The
 * dev/test server has no WABA configured, so these tests stub that one endpoint
 * to drive both sides of the flag, and stub the contact/verify endpoints so no
 * request ever reaches Meta. The real Graph API is never contacted — the
 * server-side equivalent for a live run is `SP_WHATSAPP_BASE_URL` pointed at a
 * fake Graph API.
 */

/** Serves a public-config document with the WhatsApp capability on/off. */
async function stubPublicConfig(page: Page, whatsAppEnabled: boolean) {
  await page.route("**/api/v1/config", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        posthog: { enabled: false },
        whatsapp: { enabled: whatsAppEnabled },
      }),
    });
  });
}

authTest.describe("Account Notifications — WhatsApp", () => {
  authTest(
    "hides the WhatsApp contact type when the instance capability is off",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, false);

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();

      // The phone option is always there; WhatsApp must not be.
      await expect(page.getByTestId("add-contact-type-phone")).toBeVisible();
      await expect(page.getByTestId("add-contact-type-whatsapp")).toHaveCount(0);
    },
  );

  authTest(
    "offers the WhatsApp contact type when the instance capability is on",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();

      await expect(page.getByTestId("add-contact-type-whatsapp")).toBeVisible();
    },
  );

  authTest(
    "rejects a non-E.164 WhatsApp number before it reaches the API",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      let postReceived = false;
      await page.route("**/notification-contacts", async (route) => {
        if (route.request().method() === "POST") {
          postReceived = true;
        }
        await route.continue();
      });

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      await page.getByTestId("add-contact-button").click();
      await page.getByTestId("add-contact-type-whatsapp").click();
      await page.getByTestId("add-contact-input").fill("0612345678");
      await page.getByTestId("add-contact-submit").click();

      await expect(page.getByText(/E\.164/)).toBeVisible();
      expect(postReceived).toBe(false);
    },
  );

  authTest(
    "adds a WhatsApp contact and completes the code round-trip (mocked)",
    async ({ authenticatedPage: page }) => {
      await stubPublicConfig(page, true);

      const contactUid = "contact-whatsapp-1";

      // One unverified WhatsApp route.
      await page.route("**/notification-routes**", async (route) => {
        if (route.request().method() === "GET") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              data: [
                {
                  uid: "route-whatsapp-1",
                  enabled: true,
                  position: 1,
                  contact: {
                    uid: contactUid,
                    type: "whatsapp",
                    value: "+15551230000",
                    label: "whatsapp",
                  },
                  createdAt: new Date().toISOString(),
                },
              ],
            }),
          });

          return;
        }

        await route.continue();
      });

      let confirmReceived = false;
      // Confirm endpoint is more specific — register it first.
      await page.route(/\/verify\/confirm$/, async (route) => {
        if (route.request().method() === "POST") {
          confirmReceived = true;
          await route.fulfill({ status: 204, body: "" });

          return;
        }
        await route.continue();
      });

      let verifyReceived = false;
      await page.route(/\/verify$/, async (route) => {
        if (route.request().method() === "POST") {
          verifyReceived = true;
          await route.fulfill({ status: 204, body: "" });

          return;
        }
        await route.continue();
      });

      await page.goto("orgs/test/account/notifications");
      await page.waitForLoadState("networkidle");

      // The row renders with the correctly-cased channel label.
      const list = page.getByTestId("notification-routes-list");
      await expect(list.getByText("WhatsApp", { exact: true })).toBeVisible();

      // Verify dialog: WhatsApp-specific copy, shared code round-trip.
      await page.getByTestId(`verify-phone-${contactUid}`).click();
      await expect(page.getByTestId("verify-phone-dialog")).toBeVisible();
      await expect(page.getByText(/on WhatsApp/i).first()).toBeVisible();

      await page.getByTestId("verify-send-code").click();
      await expect.poll(() => verifyReceived).toBe(true);

      await page.getByTestId("verify-code-input").fill("123456");
      await page.getByTestId("verify-confirm").click();
      await expect.poll(() => confirmReceived).toBe(true);
    },
  );

  authTest(
    "renders the WhatsApp channel and a translated failure reason in the delivery history",
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
                uid: "notif-wa-1",
                incidentUid: "incident-1",
                eventType: "incident.escalated",
                source: "escalation_user",
                channelType: "whatsapp",
                status: "skipped",
                skipReason: "whatsapp_recipient_not_on_whatsapp",
                createdAt: new Date().toISOString(),
              },
            ],
          }),
        });
      });

      await page.goto("orgs/test/me/notifications");
      await page.waitForLoadState("networkidle");

      // Correct brand casing — a plain CSS capitalize would render "Whatsapp".
      await expect(page.getByText("WhatsApp", { exact: true })).toBeVisible();

      // The reason must be prose, never the raw i18n key or the raw code.
      const reason = page.getByTestId("notification-reason-notif-wa-1");
      await expect(reason).toBeVisible();
      await expect(reason).toHaveText("Recipient is not on WhatsApp");
      await expect(reason).not.toContainText("notificationFailureReasons");
      await expect(reason).not.toContainText("whatsapp_recipient");
    },
  );
});

// A plain (unauthenticated) test: the public config document must never leak a
// WhatsApp credential, only the resolved boolean.
test("public config exposes only a WhatsApp boolean", async ({ request, baseURL }) => {
  const origin = baseURL ? new URL(baseURL).origin : API_BASE;
  const response = await request.get(`${origin}/api/v1/config`);

  expect(response.ok()).toBe(true);

  const body = (await response.json()) as Record<string, unknown>;
  const whatsapp = body.whatsapp as Record<string, unknown> | undefined;

  expect(whatsapp).toBeDefined();
  expect(typeof whatsapp?.enabled).toBe("boolean");
  // No credential, phone-number id, WABA id or template name may appear.
  expect(Object.keys(whatsapp ?? {})).toEqual(["enabled"]);
  expect(JSON.stringify(body)).not.toMatch(/accessToken|appSecret|phoneNumberId|wabaId/i);
});
