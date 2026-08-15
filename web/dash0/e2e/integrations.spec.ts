import { test, expect, API_BASE, type Page } from "./fixtures";

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

// Completely wipe all connections so list/picker state is deterministic even
// when earlier specs in the suite have created integrations. Uses a high limit
// and loops in case the list endpoint paginates.
async function deleteAllConnections(page: Page, token: string) {
  for (let guard = 0; guard < 20; guard++) {
    const list = await page.request
      .get(`${API_BASE}/api/v1/orgs/test/integrations?limit=500`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .then((r) => r.json());
    const items = (list.data ?? []) as Array<{ uid: string }>;
    if (items.length === 0) return;
    for (const c of items) await deleteConnection(page, token, c.uid);
  }
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
  test("create a Twilio channel via the form", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await deleteAllConnections(page, token);

    await page.goto("orgs/test/integrations/new?type=twilio");
    await page.waitForLoadState("networkidle");

    // The Twilio-specific panel renders.
    await expect(page.getByTestId("twilio-panel")).toBeVisible();

    await page
      .getByTestId("twilio-account-sid")
      .fill("AC00000000000000000000000000000001");
    await page.locator("#ch-twilio-token").fill("test-auth-token");
    await page.getByTestId("twilio-from-number").fill("+15551239999");

    await page.getByRole("button", { name: /create integration/i }).click();

    // On success the app navigates to the new integration's detail page.
    await page.waitForURL((url) =>
      /\/orgs\/test\/integrations\/[0-9a-f-]{36}$/.test(url.pathname),
    );

    await deleteAllConnections(page, token);
  });

  test("create webhook channel, bind to check, unbind, delete", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Clean slate so the created channel is unambiguous in the list/picker.
    await deleteAllConnections(page, token);

    // 1. Create a webhook channel via the UI
    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    await page.getByRole("link", { name: /new integration/i }).first().click();
    await page.waitForURL((url) => url.pathname.endsWith("/integrations/new"));

    // Pick webhook type
    await page.getByRole("button", { name: /^webhook\b/i }).click();

    // The "Default for new checks" toggle must start enabled on the create flow.
    await expect(page.locator("#ch-default")).toBeChecked();

    // Turn it off for this channel: default channels are auto-attached to every
    // check on creation (checks/service.go "Auto-attach default connections").
    // Left on, the channel would already be bound to the check created below,
    // and the manual toggle-on further down would *unbind* it instead — this
    // test exercises the explicit bind/unbind flow, so it must start unbound.
    await page.locator("#ch-default").click();
    await expect(page.locator("#ch-default")).not.toBeChecked();

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
    await expect(page.getByText(channelName)).toBeVisible({ timeout: 15000 });

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
    await expect(channelLabel).toBeVisible({ timeout: 15000 });
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

    // msteams (a notify-capable type) sits in the notify group, not sources.
    await expect(notifyGroup.getByTestId("pick-msteams")).toBeVisible();
    await expect(sourceGroup.getByTestId("pick-msteams")).toHaveCount(0);
  });

  test("create a Microsoft Teams channel via the form and persist its URL", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/integrations/new?type=msteams");
    await page.waitForLoadState("networkidle");

    const name = `E2E Teams ${Date.now()}`;
    const workflowUrl = "https://example.logic.azure.com/workflows/abc123";
    await page.getByLabel("Name").fill(name);
    await page.getByLabel(/webhook url/i).fill(workflowUrl);

    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/integrations\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
        url.pathname,
      ),
    );
    const uid = page.url().split("/").pop()!;

    // Reload the edit page — the workflow URL persists (round-trips through
    // the msteams webhook_url settings key on both save and reload).
    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByLabel(/webhook url/i)).toHaveValue(workflowUrl);

    await deleteConnection(page, token, uid);
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
    await deleteAllConnections(page, token);

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
    await expect(page.getByText(webhookName, { exact: true })).toBeVisible({ timeout: 15000 });
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
    await deleteAllConnections(page, token);

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

    await expect(page.getByText("No channels yet.")).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("link", { name: /create one/i })).toBeVisible();

    await deleteCheck(page, token, check.uid);
  });

  test("send-test is available on a non-webhook integration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Discord is a notify-capable type that had no test button before the
    // generic test-notification feature — it now gets one like every other
    // notification integration.
    const name = `E2E Discord ${Date.now()}`;
    const resp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "discord",
          name,
          settings: { webhook_url: "https://discord.example/hook" },
        },
      },
    );
    const integration = await resp.json();

    // Mock the delivery so the test never hits the network.
    await page.route(
      `**/api/v1/orgs/test/integrations/${integration.uid}/test`,
      async (route) => {
        if (route.request().method() === "POST") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              success: true,
              statusCode: 200,
              durationMs: 12,
            }),
          });
          return;
        }
        await route.continue();
      },
    );

    await page.goto(`orgs/test/integrations/${integration.uid}`);
    await page.waitForLoadState("networkidle");

    // The generic test section renders for this non-webhook integration.
    await expect(page.getByTestId("integration-test-section")).toBeVisible();

    await page.getByTestId("webhook-send-test").click();

    const badge = page.getByTestId("webhook-test-result");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText("200 OK");
    await expect(badge).toContainText("12 ms");

    await deleteConnection(page, token, integration.uid);
  });

  test("Save and Send-test buttons are complements of unsaved-changes state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // A plain webhook integration so the generic test section renders.
    const name = `E2E ButtonStates ${Date.now()}`;
    const resp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "webhook",
          name,
          settings: { url: "https://example.com/webhook" },
        },
      },
    );
    const integration = await resp.json();

    await page.goto(`orgs/test/integrations/${integration.uid}`);
    await page.waitForLoadState("networkidle");

    const saveBtn = page.getByTestId("integration-save");
    const sendTest = page.getByTestId("webhook-send-test");

    // 1. Freshly-loaded (clean) integration: Save disabled, Send test enabled.
    await expect(saveBtn).toBeDisabled();
    await expect(sendTest).toBeEnabled();

    // Tooltip is absent while Send test is enabled — hovering shows nothing.
    await sendTest.hover();
    await expect(page.getByTestId("webhook-send-test-tooltip")).toHaveCount(0);

    // 2. Edit the name → dirty: Save enabled, Send test disabled.
    await page.getByLabel("Name").fill(`${name} edited`);
    await expect(saveBtn).toBeEnabled();
    await expect(sendTest).toBeDisabled();

    // 3. Hovering the disabled Send test surfaces the explanatory tooltip.
    //    The trigger is the focusable wrapper span around the button.
    await sendTest.locator("..").hover();
    await expect(
      page.getByTestId("webhook-send-test-tooltip"),
    ).toBeVisible();

    // 4. Save → back to clean: Save disabled, Send test enabled again.
    await saveBtn.click();
    await page.waitForLoadState("networkidle");
    await expect(saveBtn).toBeDisabled();
    await expect(sendTest).toBeEnabled();

    await deleteConnection(page, token, integration.uid);
  });
});

// Event-type identity on the integration detail page's "Recent
// notifications" table (spec 2026-08-15-02, sections B/C). The deliveries
// endpoint is mocked because there is no deterministic way to make a real
// notification land against a freshly-created channel from this test's
// reach — same mocking pattern used for the suppressions section below.
test.describe("Integration detail — recent notifications", () => {
  test("Event column shows the labeled badge instead of a bare eventType code", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const webhookResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "webhook",
          name: `E2E Recent Notifs ${Date.now()}`,
          settings: { url: "https://example.com/webhook" },
        },
      },
    );
    const webhook = await webhookResp.json();

    await page.route(
      (url) =>
        url.pathname === "/api/v1/orgs/test/notifications" &&
        url.searchParams.get("connectionUid") === webhook.uid,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: [
              {
                uid: "33333333-3333-3333-3333-333333333333",
                incidentUid: "11111111-1111-1111-1111-111111111111",
                eventType: "incident.resolved",
                source: "check_connection",
                channelType: "webhook",
                status: "sent",
                createdAt: "2026-06-02T10:00:00Z",
                sentAt: "2026-06-02T10:00:02Z",
                user: null,
                connection: { uid: webhook.uid, name: webhook.name ?? "Webhook", type: "webhook" },
              },
            ],
          }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${webhook.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Recent notifications")).toBeVisible();

    // The translated, emoji-bearing label is visible...
    await expect(page.getByText("Incident Resolved")).toBeVisible();
    // ...and the raw eventType is no longer rendered as a bare mono-styled
    // table cell (it still lives in the badge's title attribute for
    // debugging, same as the notification detail page).
    await expect(page.locator("td.font-mono", { hasText: "incident.resolved" })).toHaveCount(0);

    await deleteConnection(page, token, webhook.uid);
  });
});

// Suppressions section on the email integration edit page (spec
// 2026-07-05-10, D4). Creation is always recipient-initiated via the public
// /unsubscribe page (never through this authenticated API), so there is no
// way to seed a real suppression row through the UI/API surface this test
// can reach — the GET/DELETE responses are mocked instead, matching the
// existing pattern used above for the Slack destination picker. This still
// exercises the real rendering, empty-state, and delete-then-refetch wiring
// of SuppressionsSection; only the two HTTP responses are fixtures.
test.describe("Email suppressions section", () => {
  test("lists suppressions and re-subscribes (deletes) one", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const emailName = `E2E Email Integration ${Date.now()}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "email",
          name: emailName,
          settings: { to: ["ops@example.com"] },
        },
      },
    );
    const integration = await createResp.json();

    const suppressionUid = "22222222-2222-2222-2222-222222222222";
    let suppressions = [
      {
        uid: suppressionUid,
        email: "unsubscribed@example.com",
        checkUid: "33333333-3333-3333-3333-333333333333",
        checkName: "Production API",
        source: "link",
        createdAt: new Date().toISOString(),
      },
      {
        uid: "44444444-4444-4444-4444-444444444444",
        email: "org-wide-unsub@example.com",
        source: "header",
        createdAt: new Date().toISOString(),
      },
    ];

    await page.route(
      `**/api/v1/orgs/test/email-suppressions`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: suppressions }),
        });
      },
    );

    await page.route(
      `**/api/v1/orgs/test/email-suppressions/${suppressionUid}`,
      async (route) => {
        if (route.request().method() === "DELETE") {
          suppressions = suppressions.filter((s) => s.uid !== suppressionUid);
          await route.fulfill({ status: 204, body: "" });

          return;
        }

        await route.continue();
      },
    );

    await page.goto(`orgs/test/integrations/${integration.uid}`);
    await page.waitForLoadState("networkidle");

    // Both rows render: the check-scoped one with its resolved check name,
    // and the org-wide one labeled "All checks".
    await expect(page.getByText("unsubscribed@example.com")).toBeVisible();
    await expect(page.getByText("Production API")).toBeVisible();
    await expect(page.getByText("org-wide-unsub@example.com")).toBeVisible();
    await expect(page.getByText("All checks")).toBeVisible();

    // Re-subscribe (delete) the check-scoped row via its ghost Trash2 button.
    await page
      .getByRole("button", { name: /re-subscribe unsubscribed@example.com/i })
      .click();

    // The row disappears once the list is refetched after the mutation.
    await expect(page.getByText("unsubscribed@example.com")).toHaveCount(0, {
      timeout: 15000,
    });
    // The other recipient is unaffected.
    await expect(page.getByText("org-wide-unsub@example.com")).toBeVisible();

    await deleteConnection(page, token, integration.uid);
  });

  test("shows an empty state when no one has unsubscribed", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const emailName = `E2E Email Integration Empty ${Date.now()}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "email",
          name: emailName,
          settings: { to: ["ops@example.com"] },
        },
      },
    );
    const integration = await createResp.json();

    await page.route(
      `**/api/v1/orgs/test/email-suppressions`,
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: [] }),
        });
      },
    );

    await page.goto(`orgs/test/integrations/${integration.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByText(/no one has unsubscribed/i),
    ).toBeVisible();

    await deleteConnection(page, token, integration.uid);
  });
});

// Multi-address recipients input (spec 2026-07-14-06). The email panel's
// Textarea was replaced by a RecipientsInput chip control that parses any
// separator (space/comma/semicolon/newline) via parseEmailList and blocks
// save while any chip fails isValidEmail.
test.describe("Email integration recipients", () => {
  test("space-separated addresses become two chips and persist on reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/integrations/new?type=email");
    await page.waitForLoadState("networkidle");

    const name = `E2E Email Multi ${Date.now()}`;
    await page.getByLabel("Name").fill(name);

    // Type a single space-separated string into the chip input's free-text
    // field, then blur — commits both tokens via parseEmailList.
    const recipientsField = page.getByTestId("email-recipients-input");
    await recipientsField.fill("a@x.example.com b@y.example.com");
    await page.getByLabel("Name").click(); // blur the recipients field

    await expect(page.getByTestId("email-recipients-chip-0")).toContainText(
      "a@x.example.com",
    );
    await expect(page.getByTestId("email-recipients-chip-1")).toContainText(
      "b@y.example.com",
    );

    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/integrations\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
        url.pathname,
      ),
    );
    const uid = page.url().split("/").pop()!;

    // Reload the edit page — both recipients persist as chips.
    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("email-recipients-chip-0")).toContainText(
      "a@x.example.com",
    );
    await expect(page.getByTestId("email-recipients-chip-1")).toContainText(
      "b@y.example.com",
    );

    await deleteConnection(page, token, uid);
  });

  test("comma-separated paste yields two distinct recipients", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/integrations/new?type=email");
    await page.waitForLoadState("networkidle");
    await page
      .getByLabel("Name")
      .fill(`E2E Email Comma ${Date.now()}`);

    const recipientsField = page.getByTestId("email-recipients-input");
    // Simulate a paste event carrying a comma-separated blob.
    await recipientsField.evaluate((el, text) => {
      const input = el as HTMLInputElement;
      const dt = new DataTransfer();
      dt.setData("text", text);
      const event = new ClipboardEvent("paste", {
        clipboardData: dt,
        bubbles: true,
        cancelable: true,
      });
      input.dispatchEvent(event);
    }, "c@x.example.com, d@y.example.com");

    await expect(page.getByTestId("email-recipients-chip-0")).toContainText(
      "c@x.example.com",
    );
    await expect(page.getByTestId("email-recipients-chip-1")).toContainText(
      "d@y.example.com",
    );
  });

  test("invalid address renders a destructive chip and blocks create", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Uses the create flow (rather than edit) so the assertion on the submit
    // button's disabled state is driven purely by recipient validity, not
    // entangled with the edit page's separate isDirty gating.
    await page.goto("orgs/test/integrations/new?type=email");
    await page.waitForLoadState("networkidle");
    await page.getByLabel("Name").fill(`E2E Email Invalid ${Date.now()}`);

    const recipientsField = page.getByTestId("email-recipients-input");
    const createBtn = page.getByRole("button", {
      name: /create integration/i,
    });

    await recipientsField.fill("ops@example.com");
    await recipientsField.press("Enter");
    await expect(page.getByTestId("email-recipients-chip-0")).toContainText(
      "ops@example.com",
    );
    await expect(createBtn).toBeEnabled();

    await recipientsField.fill("not-an-email");
    await recipientsField.press("Enter");

    // The invalid chip is visibly flagged (destructive + inline error) and
    // blocks Create, without dropping the valid entry already committed.
    await expect(page.getByTestId("email-recipients-chip-1")).toContainText(
      "not-an-email",
    );
    await expect(page.getByTestId("email-recipients-error")).toBeVisible();
    await expect(createBtn).toBeDisabled();

    // Removing the invalid chip restores validity; the valid entry remains.
    await page.getByTestId("email-recipients-chip-remove-1").click();
    await expect(page.getByTestId("email-recipients-error")).toHaveCount(0);
    await expect(page.getByTestId("email-recipients-chip-0")).toContainText(
      "ops@example.com",
    );
    await expect(createBtn).toBeEnabled();
  });
});
