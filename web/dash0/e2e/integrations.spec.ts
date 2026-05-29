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
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
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

    // Slack channels are install-only — the manual POST /channels endpoint
    // rejects type=slack. To exercise the connected picker we mock the channel
    // GET so the edit page renders as if an OAuth-installed channel exists.
    const channelUid = "11111111-1111-1111-1111-111111111111";

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
              name: "Test Workspace",
              enabled: true,
              isDefault: false,
              settings: {
                team_id: "T0123",
                team_name: "Test Workspace",
              },
              settingsPrivateKeys: ["access_token"],
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            }),
          });

          return;
        }

        await route.continue();
      },
    );

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
      `**/api/v1/orgs/test/integrations/${channelUid}`,
      async (route) => {
        if (route.request().method() === "PATCH") {
          const body = route.request().postDataJSON() as Record<string, unknown>;
          patchBody = body;
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              uid: channelUid,
              type: "slack",
              name: "Test Workspace",
              enabled: true,
              isDefault: false,
              settings: { team_id: "T0123", team_name: "Test Workspace" },
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            }),
          });

          return;
        }

        await route.fallback();
      },
    );

    // Navigate to the channel edit page
    await page.goto(`orgs/test/integrations/${channelUid}`);
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
  });
});

test.describe("Notification Channels", () => {
  test("create webhook channel, bind to check, unbind, delete", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // 1. Create a webhook channel via the UI
    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    await page.getByRole("link", { name: /new integration/i }).first().click();
    await page.waitForURL((url) => url.pathname.endsWith("/integrations/new"));

    // Pick webhook type
    await page.getByRole("button", { name: /^webhook\b/i }).click();

    const channelName = `E2E Webhook ${Date.now()}`;
    await page.getByLabel("Name").fill(channelName);
    await page
      .getByLabel(/webhook url/i)
      .fill("https://example.com/webhook");
    await page.getByRole("button", { name: /create integration/i }).click();

    // Should land on detail page (UUID, not /channels/new — which also matches [^/]+).
    await page.waitForURL((url) =>
      /\/integrations\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
        url.pathname,
      ),
    );
    const detailUrl = page.url();
    const channelUid = detailUrl.split("/").pop()!;

    // 2. Channel appears in the list
    await page.goto("orgs/test/integrations");
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
    await page.goto(`orgs/test/integrations/${channelUid}`);
    await page.waitForLoadState("networkidle");
    await page.getByRole("button", { name: /delete integration/i }).click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: /delete/i }).click();

    await page.waitForURL((url) => url.pathname.endsWith("/integrations"));
    await expect(page.getByText(channelName)).toHaveCount(0);

    // Cleanup
    await deleteCheck(page, token, check.uid);
  });

  test("new-integration picker groups notification channels and data sources", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new");
    await page.waitForLoadState("networkidle");

    // The picker is split into two capability groups.
    const notifyGroup = page.getByTestId("group-notify");
    const sourceGroup = page.getByTestId("group-source");
    await expect(notifyGroup).toBeVisible();
    await expect(sourceGroup).toBeVisible();

    // A notify-capable type (webhook) sits in the notify group, not sources.
    await expect(notifyGroup.getByTestId("pick-webhook")).toBeVisible();
    await expect(sourceGroup.getByTestId("pick-webhook")).toHaveCount(0);

    // Freebox (a data source) sits in the source group, not notify.
    await expect(sourceGroup.getByTestId("pick-freebox")).toBeVisible();
    await expect(notifyGroup.getByTestId("pick-freebox")).toHaveCount(0);

    // webpush (a notify-capable type) sits in the notify group, not sources.
    await expect(notifyGroup.getByTestId("pick-webpush")).toBeVisible();
    await expect(sourceGroup.getByTestId("pick-webpush")).toHaveCount(0);
  });

  test("webpush channel panel renders subscribe button and empty device list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new?type=webpush");
    await page.waitForLoadState("networkidle");

    // The WebPushChannelPanel should be rendered.
    const panel = page.getByTestId("webpush-channel-panel");
    await expect(panel).toBeVisible();

    // The empty-device message should appear.
    await expect(
      panel.getByText(/No devices subscribed yet/i),
    ).toBeVisible();
  });

  test("freebox source is hidden from the check Notify via picker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Wipe existing connections for a deterministic picker.
    const existing = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/integrations`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .then((r) => r.json());
    for (const c of existing.data ?? []) {
      await deleteConnection(page, token, c.uid);
    }

    // Create one notify integration (webhook) and one source (freebox).
    const webhookName = `E2E Notify Webhook ${Date.now()}`;
    const webhookResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "webhook",
          name: webhookName,
          settings: { url: "https://example.com/webhook" },
        },
      },
    );
    const webhook = await webhookResp.json();

    const freeboxName = `E2E Freebox Source ${Date.now()}`;
    const freeboxResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { type: "freebox", name: freeboxName },
      },
    );
    const freebox = await freeboxResp.json();

    const checkResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "http",
          name: `E2E NotifyFilter ${Date.now()}`,
          config: { url: `https://example.com/${Date.now()}` },
          period: "00:05:00",
        },
      },
    );
    const check = await checkResp.json();

    await page.goto(`orgs/test/checks/${check.uid}/edit`);
    await page.waitForLoadState("networkidle");

    // The notify-capable webhook appears; the freebox source does not.
    await expect(page.getByText(webhookName, { exact: true })).toBeVisible();
    await expect(page.getByText(freeboxName, { exact: true })).toHaveCount(0);

    // The API also rejects binding the freebox source as a notify target.
    const bindResp = await page.request.put(
      `${API_BASE}/api/v1/orgs/test/checks/${check.uid}/channels`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { connectionUids: [freebox.uid] },
      },
    );
    expect(bindResp.status()).toBe(400);
    const bindErr = await bindResp.json();
    expect(bindErr.code).toBe("VALIDATION_ERROR");

    // Cleanup.
    await deleteCheck(page, token, check.uid);
    await deleteConnection(page, token, webhook.uid);
    await deleteConnection(page, token, freebox.uid);
  });

  test("empty Notify via shows create-channel link", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Wipe any existing connections so the empty state shows
    const list = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/integrations`, {
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
