import { test, expect, API_BASE, type Page } from "./fixtures";

// The SMS panel explains where an organization's text messages come from:
// the instance's own credentials (server-provided, the default that needs no
// setup) or the organization's own Twilio integration (bring-your-own, which
// overrides the instance). The explanation IS the feature — without it an
// admin cannot tell "SMS already works" from "SMS is not configured", and has
// no way to know that adding a Twilio integration silently overrode the
// instance provider or what deleting it would do.
//
// The instance side is mocked at `GET /api/v1/config` so the assertions do not
// depend on how the machine running the suite happens to be configured.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteAllConnections(page: Page, token: string) {
  for (let guard = 0; guard < 20; guard++) {
    const list = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/integrations?limit=500`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .then((r) => r.json());
    const items = (list.data ?? []) as Array<{ uid: string }>;
    if (items.length === 0) return;
    for (const c of items) {
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/integrations/${c.uid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
    }
  }
}

/** Forces the instance-level SMS capability the dashboard sees. */
async function mockInstanceSMS(
  page: Page,
  sms: { enabled: boolean; sender?: string; provider?: string; voiceEnabled: boolean },
) {
  await page.route("**/api/v1/config", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        posthog: { enabled: false },
        whatsapp: { enabled: false },
        telegram: { enabled: false },
        sms,
      }),
    });
  });
}

test.describe("SMS mode panel", () => {
  test("server-provided is the default and is explained", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await deleteAllConnections(page, token);

    await mockInstanceSMS(page, {
      enabled: true,
      sender: "SolidPing",
      provider: "ovh",
      voiceEnabled: true,
    });

    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    const panel = page.getByTestId("sms-mode-panel");
    await expect(panel).toBeVisible();
    await expect(page.getByTestId("sms-mode-badge")).toHaveText(
      /provided by this instance/i,
    );
    // The sender is shown; a credential never is.
    await expect(page.getByTestId("sms-mode-explanation")).toContainText(
      "SolidPing",
    );
    // The bring-your-own escape hatch is offered, not hidden.
    await expect(panel).toContainText(/own Twilio integration/i);
    await expect(page.getByTestId("sms-add-own-twilio")).toBeVisible();
  });

  test("no instance provider and no integration reads as unavailable", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await deleteAllConnections(page, token);

    await mockInstanceSMS(page, { enabled: false, voiceEnabled: false });

    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("sms-mode-badge")).toHaveText(/not available/i);
    await expect(page.getByTestId("sms-mode-explanation")).toContainText(
      /cannot be sent/i,
    );
  });

  test("bring-your-own says it overrides the instance and what deletion does", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await deleteAllConnections(page, token);

    await mockInstanceSMS(page, {
      enabled: true,
      sender: "SolidPing",
      provider: "ovh",
      voiceEnabled: true,
    });

    await page.goto("orgs/test/integrations/new?type=twilio");
    await page.waitForLoadState("networkidle");
    await page
      .getByTestId("twilio-account-sid")
      .fill("AC00000000000000000000000000000001");
    await page.locator("#ch-twilio-token").fill("test-auth-token");
    await page.getByTestId("twilio-from-number").fill("+15551239999");
    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/orgs\/test\/integrations\/[0-9a-f-]{36}$/.test(url.pathname),
    );

    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("sms-mode-badge")).toHaveText(/your own account/i);

    const explanation = page.getByTestId("sms-mode-explanation");
    await expect(explanation).toContainText(/overrides the instance provider/i);

    // The panel must state what deleting the integration does — falling back
    // to the instance here, since the instance provides SMS.
    await expect(page.getByTestId("sms-mode-panel")).toContainText(
      /falls back to the SMS provided by this instance/i,
    );

    // Deleting it returns the org to the server-provided mode.
    await deleteAllConnections(page, token);
    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("sms-mode-badge")).toHaveText(
      /provided by this instance/i,
    );
  });
});
