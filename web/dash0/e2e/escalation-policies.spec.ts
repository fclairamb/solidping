import { test, expect, API_BASE, type Page } from "./fixtures";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteAllEscalationPolicies(page: Page, token: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/escalation-policies`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  const body = await resp.json();
  for (const p of body.data ?? []) {
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/escalation-policies/${p.uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  }
}

async function createChannel(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string; name: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/channels`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      type: "webhook",
      settings: { url: "https://example.com/hook" },
    },
  });
  const body = await resp.json();
  return { uid: body.uid, name: body.name };
}

async function deleteChannel(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/channels/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function createOnCallSchedule(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name,
        timezone: "UTC",
        rotationType: "weekly",
        handoffTime: "09:00",
        handoffWeekday: 1,
        startAt: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
        userUids: [],
      },
    },
  );
  return resp.json();
}

async function deleteOnCallSchedule(page: Page, token: string, uid: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
}

async function createEscalationPolicy(
  page: Page,
  token: string,
  name: string,
  steps: unknown[] = [],
): Promise<{ uid: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/escalation-policies`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { name, repeatMax: 0, steps },
    },
  );
  return resp.json();
}

test.describe("Escalation policy editor", () => {
  test("step targets seeded via API appear selected in the edit UI and survive a re-save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();

    const channel = await createChannel(page, token, `E2E Hook ${stamp}`);
    const schedule = await createOnCallSchedule(
      page,
      token,
      `E2E Sched ${stamp}`,
    );
    // Test org user: test@test.com — get their userUid via the members API.
    const membersResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/members`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const members = await membersResp.json();
    const userUid = (members.data ?? [])[0]?.userUid as string;
    expect(userUid).toBeTruthy();

    const policy = await createEscalationPolicy(
      page,
      token,
      `E2E Policy ${stamp}`,
      [
        {
          delayMinutes: 0,
          targets: [{ type: "connection", targetUid: channel.uid }],
        },
      ],
    );

    try {
      // Navigate to the edit page (addressed by uid).
      await page.goto(`orgs/test/escalation-policies/${policy.uid}`);
      await page.waitForLoadState("networkidle");

      // The first step's type selector must show "Notification connection".
      const typeCombobox = page.getByRole("combobox").first();
      await expect(typeCombobox).toContainText("Notification connection");

      // The secondary selector must show the channel name.
      const secondaryCombobox = page.getByRole("combobox").nth(1);
      await expect(secondaryCombobox).toContainText(channel.name);

      // Save without changes, then reload and verify selection is still there.
      await page.getByRole("button", { name: /save/i }).click();
      await page.waitForLoadState("networkidle");
      await page.reload();
      await page.waitForLoadState("networkidle");

      await expect(page.getByRole("combobox").first()).toContainText(
        "Notification connection",
      );
      await expect(page.getByRole("combobox").nth(1)).toContainText(channel.name);

      // Swap to "All admins" via the UI, save, reload, verify.
      await page.getByRole("combobox").first().click();
      await page.getByRole("option", { name: "All admins" }).click();
      await page.getByRole("button", { name: /save/i }).click();
      await page.waitForLoadState("networkidle");
      await page.reload();
      await page.waitForLoadState("networkidle");

      await expect(page.getByRole("combobox").first()).toContainText("All admins");
      // No secondary combobox for all_admins.
      await expect(page.getByRole("combobox")).toHaveCount(1);

      // Swap to "User", pick the member, save, verify.
      await page.getByRole("combobox").first().click();
      await page.getByRole("option", { name: "User" }).click();
      const userCombobox = page.getByRole("combobox").nth(1);
      await userCombobox.click();
      await page.getByRole("option").first().click();
      await page.getByRole("button", { name: /save/i }).click();
      await page.waitForLoadState("networkidle");
      await page.reload();
      await page.waitForLoadState("networkidle");

      await expect(page.getByRole("combobox").first()).toContainText("User");
      // The secondary must show the member (not empty).
      await expect(page.getByRole("combobox").nth(1)).not.toContainText(
        /pick a user/i,
      );

      // Swap to "On-call schedule", pick the schedule, save, verify.
      await page.getByRole("combobox").first().click();
      await page.getByRole("option", { name: "On-call schedule" }).click();
      await page.getByRole("combobox").nth(1).click();
      await page.getByRole("option", { name: `E2E Sched ${stamp}` }).click();
      await page.getByRole("button", { name: /save/i }).click();
      await page.waitForLoadState("networkidle");
      await page.reload();
      await page.waitForLoadState("networkidle");

      await expect(page.getByRole("combobox").first()).toContainText("On-call schedule");
      await expect(page.getByRole("combobox").nth(1)).toContainText(
        `E2E Sched ${stamp}`,
      );
    } finally {
      await deleteAllEscalationPolicies(page, token);
      await deleteChannel(page, token, channel.uid);
      await deleteOnCallSchedule(page, token, schedule.uid);
    }
  });

  test("header: Back button is immediately to the left of the Delete button", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();

    const policy = await createEscalationPolicy(
      page,
      token,
      `E2E Header ${stamp}`,
    );

    try {
      await page.goto(`orgs/test/escalation-policies/${policy.uid}`);
      await page.waitForLoadState("networkidle");

      const backBtn = page.getByRole("link", { name: /back/i });
      const deleteBtn = page.getByRole("button", { name: /delete/i });

      await expect(backBtn).toBeVisible();
      await expect(deleteBtn).toBeVisible();

      const backBox = await backBtn.boundingBox();
      const deleteBox = await deleteBtn.boundingBox();

      expect(backBox).not.toBeNull();
      expect(deleteBox).not.toBeNull();

      // Back must be to the left of Delete (right edge of Back < left edge of Delete).
      expect(backBox!.x + backBox!.width).toBeLessThan(deleteBox!.x);
      // Both must be on the same row (vertical centers within 20px of each other).
      const backCenterY = backBox!.y + backBox!.height / 2;
      const deleteCenterY = deleteBox!.y + deleteBox!.height / 2;
      expect(Math.abs(backCenterY - deleteCenterY)).toBeLessThan(20);
    } finally {
      await deleteAllEscalationPolicies(page, token);
    }
  });
});
