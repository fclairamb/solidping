import { test, expect, type Page } from "./fixtures";

const API_BASE = "http://localhost:4000";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteConnection(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/channels/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Slack destination picker", () => {
  test("selects a Slack channel via mocked destinations endpoint", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create a fake Slack channel via API (type=slack, minimal settings)
    const channelResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/channels`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "slack",
          name: `E2E Slack Picker ${Date.now()}`,
          settings: {
            team_id: "T0123",
            team_name: "Test Workspace",
            bot_user_id: "B0123",
            access_token: "xoxb-fake",
            installed_by_user_id: "U0001",
            scopes: [],
          },
        },
      },
    );
    const channel = await channelResp.json();
    const channelUid: string = channel.uid;

    // Mock the /slack/destinations endpoint before navigating
    await page.route(
      `**/api/v1/orgs/test/channels/${channelUid}/slack/destinations`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            channels: [
              { id: "C0123ABCDE", name: "alerts", isPrivate: false, isMember: true },
              { id: "C9999", name: "ops", isPrivate: false, isMember: false },
            ],
            users: [],
          }),
        });
      },
    );

    // Intercept the PATCH request to capture the body
    let patchBody: Record<string, unknown> = {};
    await page.route(
      `**/api/v1/orgs/test/channels/${channelUid}`,
      async (route) => {
        if (route.request().method() === "PATCH") {
          const body = route.request().postDataJSON() as Record<string, unknown>;
          patchBody = body;
        }
        await route.continue();
      },
    );

    // Navigate to the channel edit page
    await page.goto(`orgs/test/channels/${channelUid}`);
    await page.waitForLoadState("networkidle");

    // Open the channel combobox and pick "alerts"
    const combobox = page.getByTestId("slack-channel-combobox");
    await combobox.click();

    const option = page.getByTestId("slack-channel-option-C0123ABCDE");
    await option.click();

    // Save
    await page.getByRole("button", { name: /save/i }).click();
    await page.waitForLoadState("networkidle");

    // Verify the PATCH body contains the expected fields
    const settings = patchBody.settings as Record<string, unknown> | undefined;
    expect(settings?.channel_id).toBe("C0123ABCDE");
    expect(settings?.destination_type).toBe("channel");

    // Cleanup
    await deleteConnection(page, token, channelUid);
  });
});

test.describe("Notification Channels", () => {
  test("create webhook channel, bind to check, unbind, delete", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // 1. Create a webhook channel via the UI
    await page.goto("orgs/test/channels");
    await page.waitForLoadState("networkidle");

    await page.getByRole("link", { name: /new channel/i }).first().click();
    await page.waitForURL((url) => url.pathname.endsWith("/channels/new"));

    // Pick webhook type
    await page.getByRole("button", { name: /^webhook\b/i }).click();

    const channelName = `E2E Webhook ${Date.now()}`;
    await page.getByLabel("Name").fill(channelName);
    await page
      .getByLabel(/webhook url/i)
      .fill("https://example.com/webhook");
    await page.getByRole("button", { name: /create channel/i }).click();

    // Should land on detail page (UUID, not /channels/new — which also matches [^/]+).
    await page.waitForURL((url) =>
      /\/channels\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
        url.pathname,
      ),
    );
    const detailUrl = page.url();
    const channelUid = detailUrl.split("/").pop()!;

    // 2. Channel appears in the list
    await page.goto("orgs/test/channels");
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(channelName)).toBeVisible();

    // 3. Create a check via API and bind the channel via the edit UI
    const checkResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "http",
          name: `E2E Check ${Date.now()}`,
          config: { url: `https://example.com/${Date.now()}` },
          period: "00:05:00",
        },
      },
    );
    const check = await checkResp.json();

    await page.goto(`orgs/test/checks/${check.uid}/edit`);
    await page.waitForLoadState("networkidle");

    // Toggle the new channel on
    const channelLabel = page.getByText(channelName, { exact: true });
    await expect(channelLabel).toBeVisible();
    await channelLabel.click();
    await page.getByTestId("check-submit-button").click();

    // Wait for redirect away from edit page
    await page.waitForURL((url) => !url.pathname.endsWith("/edit"));

    // Verify the binding via API
    const bindings = await page.request
      .get(
        `${API_BASE}/api/v1/orgs/test/checks/${check.uid}/channels`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      .then((r) => r.json());
    expect(bindings.data?.some((b: { uid: string }) => b.uid === channelUid)).toBe(
      true,
    );

    // 4. Unbind via the edit page
    await page.goto(`orgs/test/checks/${check.uid}/edit`);
    await page.waitForLoadState("networkidle");
    await page.getByText(channelName, { exact: true }).click();
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL((url) => !url.pathname.endsWith("/edit"));

    const afterUnbind = await page.request
      .get(
        `${API_BASE}/api/v1/orgs/test/checks/${check.uid}/channels`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      .then((r) => r.json());
    expect(
      (afterUnbind.data ?? []).some((b: { uid: string }) => b.uid === channelUid),
    ).toBe(false);

    // 5. Delete the channel via the UI
    await page.goto(`orgs/test/channels/${channelUid}`);
    await page.waitForLoadState("networkidle");
    await page.getByRole("button", { name: /delete channel/i }).click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: /delete/i }).click();

    await page.waitForURL((url) => url.pathname.endsWith("/channels"));
    await expect(page.getByText(channelName)).toHaveCount(0);

    // Cleanup
    await deleteCheck(page, token, check.uid);
  });

  test("empty Notify via shows create-channel link", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Wipe any existing connections so the empty state shows
    const list = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/channels`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .then((r) => r.json());
    for (const c of list.data ?? []) {
      await deleteConnection(page, token, c.uid);
    }

    const checkResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "http",
          name: `E2E NotifyEmpty ${Date.now()}`,
          config: { url: `https://example.com/${Date.now()}` },
          period: "00:05:00",
        },
      },
    );
    const check = await checkResp.json();

    await page.goto(`orgs/test/checks/${check.uid}/edit`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("No channels yet.")).toBeVisible();
    await expect(page.getByRole("link", { name: /create one/i })).toBeVisible();

    await deleteCheck(page, token, check.uid);
  });
});
