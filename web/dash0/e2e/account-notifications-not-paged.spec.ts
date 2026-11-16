import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-11-16-02: personal methods only fire through an escalation policy,
// so Account -> Notifications warns when none pages the caller.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

function auth(token: string) {
  return { headers: { Authorization: `Bearer ${token}` } };
}

async function reset(page: Page, token: string) {
  await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
    ...auth(token),
    data: { defaultEscalationPolicyUid: "" },
  });
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/escalation-policies`, auth(token));
  for (const p of (await resp.json()).data ?? []) {
    await page.request.delete(`${API_BASE}/api/v1/orgs/test/escalation-policies/${p.uid}`, auth(token));
  }
}

test.describe("Account notifications: not-paged warning", () => {
  test("shows for an org without policy and disappears once the org default pages the user", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await reset(page, token);

    try {
      await page.goto("orgs/test/account/notifications");
      const alert = page.getByTestId("notifications-not-paged-alert");
      await expect(alert).toBeVisible();
      await expect(page.getByTestId("notifications-not-paged-link")).toBeVisible();

      const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/escalation-policies`, {
        ...auth(token),
        data: {
          name: "Page admins",
          repeatMax: 0,
          steps: [{ delaySeconds: 0, targets: [{ type: "all_admins" }] }],
        },
      });
      const policy = await created.json();
      await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
        ...auth(token),
        data: { defaultEscalationPolicyUid: policy.uid },
      });

      const loaded = page.waitForResponse(/users\/me\/notification-routes$/);
      await page.reload();
      const payload = await (await loaded).json();
      expect(payload.paging.coveredBy).toBe("org_default");
      await expect(alert).toHaveCount(0);
    } finally {
      await reset(page, token);
    }
  });

  test("the integrations page points to personal channels", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/integrations");
    await expect(page.getByTestId("integrations-personal-channels-note")).toBeVisible();
    await page.getByTestId("integrations-personal-channels-link").click();
    await expect(page).toHaveURL(/account\/notifications/);
  });
});
