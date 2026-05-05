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
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/connections/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

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
    await page.getByRole("button", { name: /^webhook$/i }).click();

    const channelName = `E2E Webhook ${Date.now()}`;
    await page.getByLabel("Name").fill(channelName);
    await page
      .getByLabel(/webhook url/i)
      .fill("https://example.com/webhook");
    await page.getByRole("button", { name: /create channel/i }).click();

    // Should land on detail page
    await page.waitForURL((url) => /\/channels\/[^/]+$/.test(url.pathname));
    const detailUrl = page.url();
    const connectionUid = detailUrl.split("/").pop()!;

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
        `${API_BASE}/api/v1/orgs/test/checks/${check.uid}/connections`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      .then((r) => r.json());
    expect(bindings.data?.some((b: { uid: string }) => b.uid === connectionUid)).toBe(
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
        `${API_BASE}/api/v1/orgs/test/checks/${check.uid}/connections`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      .then((r) => r.json());
    expect(
      (afterUnbind.data ?? []).some((b: { uid: string }) => b.uid === connectionUid),
    ).toBe(false);

    // 5. Delete the channel via the UI
    await page.goto(`orgs/test/channels/${connectionUid}`);
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
      .get(`${API_BASE}/api/v1/orgs/test/connections`, {
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
