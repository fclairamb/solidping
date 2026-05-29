import { test, expect } from "./fixtures";

// Slack channels are install-only: the New Channel form must offer an Install
// CTA (full-page redirect to the OAuth install flow) instead of a create form,
// and the edit page of an unconnected (tokenless) Slack channel must show a
// not-connected CTA without ever calling the /slack/destinations endpoint.
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

    // Clicking Install triggers a navigation to the install endpoint. Stub it
    // so we can assert the target without leaving the app.
    let installRequested = false;
    await page.route(
      "**/api/v1/integrations/slack/install*",
      async (route) => {
        installRequested = true;
        const url = route.request().url();
        expect(url).toContain("/api/v1/integrations/slack/install");
        expect(url).toContain("source=dashboard");
        await route.fulfill({ status: 204, body: "" });
      },
    );

    await installButton.click();
    await expect.poll(() => installRequested).toBe(true);
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
  });
});
