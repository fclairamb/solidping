import { test, expect } from "./fixtures";

// Slack channels are install-only: the New Channel form must offer an Install
// CTA instead of a create form, and the edit page of an unconnected
// (tokenless) Slack channel must show a not-connected CTA without ever
// calling the /slack/destinations endpoint. Both CTAs mint their OAuth
// authorize URL via the authenticated, org-scoped
// POST /orgs/:org/integrations/slack/install-url endpoint (spec
// 2026-07-05-01) rather than the legacy unauthenticated
// GET /api/v1/integrations/slack/install?org=...&channelUid=... link, then
// navigate the browser there.
const FAKE_AUTHORIZE_URL =
  "https://slack.com/oauth/v2/authorize?client_id=fake&state=fake-state";

test.describe("Slack install-only channel", () => {
  test("New Channel → Slack shows Install CTA, no create form", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new");
    await page.waitForLoadState("networkidle");

    // Pick the Slack tile.
    await page.getByTestId("pick-slack").click();

    // Install button is visible and points at the OAuth install endpoint.
    const installButton = page.getByTestId("slack-install");
    await expect(installButton).toBeVisible();
    await expect(installButton).toHaveText(/install slack app/i);

    // No "Create channel" submit button is rendered for Slack.
    await expect(
      page.getByRole("button", { name: /create integration/i }),
    ).toHaveCount(0);

    // Clicking Install POSTs to the org-scoped install-url endpoint (no
    // channelUid — this is the new-integration tile, not a channel edit
    // page), then navigates to the returned authorize URL. Stub both legs.
    let installUrlRequested = false;
    await page.route(
      "**/api/v1/orgs/test/integrations/slack/install-url",
      async (route) => {
        expect(route.request().method()).toBe("POST");
        const body = route.request().postDataJSON() as Record<
          string,
          unknown
        >;
        expect(body.channelUid).toBeUndefined();
        installUrlRequested = true;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ url: FAKE_AUTHORIZE_URL }),
        });
      },
    );
    await page.route("https://slack.com/**", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/html",
        body: "<html><body>fake slack authorize page</body></html>",
      });
    });

    await installButton.click();
    await expect.poll(() => installUrlRequested).toBe(true);
    await page.waitForURL(FAKE_AUTHORIZE_URL);
  });

  test("Unconnected Slack edit page shows CTA, no destinations call", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // A tokenless Slack stub: settings have no team_id (and no access_token).
    const channelUid = "22222222-2222-2222-2222-222222222222";

    await page.route(
      `**/api/v1/orgs/test/integrations/${channelUid}`,
      async (route) => {
        if (route.request().method() === "GET") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              uid: channelUid,
              type: "slack",
              name: "Slack",
              enabled: true,
              isDefault: false,
              settings: {},
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            }),
          });

          return;
        }

        await route.continue();
      },
    );

    // Track whether the destinations endpoint is ever requested — it must not be.
    let destinationsCalled = false;
    await page.route(
      `**/api/v1/orgs/test/channels/${channelUid}/slack/destinations`,
      async (route) => {
        destinationsCalled = true;
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            title: "Slack channel is not connected — install the Slack app",
            code: "CHANNEL_NOT_CONNECTED",
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${channelUid}`);
    await page.waitForLoadState("networkidle");

    // The not-connected CTA is visible with the install button.
    await expect(page.getByTestId("slack-not-connected")).toBeVisible();
    const installButton = page
      .getByTestId("slack-not-connected")
      .getByTestId("slack-install");
    await expect(installButton).toBeVisible();
    await expect(installButton).toHaveText(/install slack app/i);

    // The picker UI (combobox) must not render.
    await expect(page.getByTestId("slack-channel-combobox")).toHaveCount(0);

    // And the destinations endpoint was never hit (gated hook).
    expect(destinationsCalled).toBe(false);

    // Clicking Install POSTs to the org-scoped install-url endpoint WITH
    // this channel's UID (so the callback updates this specific channel
    // instead of creating a new one), then navigates to the authorize URL.
    let installUrlBody: Record<string, unknown> = {};
    await page.route(
      "**/api/v1/orgs/test/integrations/slack/install-url",
      async (route) => {
        expect(route.request().method()).toBe("POST");
        installUrlBody = route.request().postDataJSON() as Record<
          string,
          unknown
        >;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ url: FAKE_AUTHORIZE_URL }),
        });
      },
    );
    await page.route("https://slack.com/**", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/html",
        body: "<html><body>fake slack authorize page</body></html>",
      });
    });

    await installButton.click();
    await expect.poll(() => installUrlBody.channelUid).toBe(channelUid);
    await page.waitForURL(FAKE_AUTHORIZE_URL);
  });
});
