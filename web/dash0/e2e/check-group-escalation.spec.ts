import { test, expect, API_BASE, type Page } from "./fixtures";

// End-to-end coverage for check-group escalation policy management (spec
// 2026-07-20-01): the dedicated /check-groups/:uid/edit route (name,
// description, escalation policy picker with the shorter "group" inherit
// chain), the checks-list group-header escalation indicator, and the
// check form's inherit label reflecting a group-level assignment.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

function auth(token: string) {
  return { headers: { Authorization: `Bearer ${token}` } };
}

async function createPolicy(
  page: Page,
  token: string,
  name: string,
  steps: unknown[] = [],
): Promise<{ uid: string; name: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/escalation-policies`,
    { ...auth(token), data: { name, repeatMax: 0, steps } },
  );
  const body = await resp.json();
  return { uid: body.uid, name };
}

async function createGroup(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string; name: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/check-groups`,
    { ...auth(token), data: { name } },
  );
  const body = await resp.json();
  return { uid: body.uid, name };
}

async function getGroup(
  page: Page,
  token: string,
  uid: string,
): Promise<{ escalationPolicyUid?: string | null; description?: string }> {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/check-groups/${uid}`,
    auth(token),
  );
  return resp.json();
}

async function createCheck(
  page: Page,
  token: string,
  name: string,
  checkGroupUid: string,
): Promise<{ uid: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    ...auth(token),
    data: {
      name,
      type: "http",
      config: { url: `https://example.com/${Date.now()}` },
      checkGroupUid,
    },
  });
  return resp.json();
}

async function setOrgDefault(page: Page, token: string, uid: string) {
  await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
    ...auth(token),
    data: { defaultEscalationPolicyUid: uid },
  });
}

async function clearOrgDefault(page: Page, token: string) {
  await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
    ...auth(token),
    data: { defaultEscalationPolicyUid: "" },
  });
}

async function deleteAllPolicies(page: Page, token: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/escalation-policies`,
    auth(token),
  );
  const body = await resp.json();
  for (const p of body.data ?? []) {
    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/escalation-policies/${p.uid}`,
      auth(token),
    );
  }
}

async function deleteGroup(page: Page, token: string, uid: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/check-groups/${uid}`,
    auth(token),
  );
}

test.describe("Check group escalation policy", () => {
  test("edit route assigns a policy: check form inherit label reflects it, and the group header shows the indicator", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();

    const orgPolicy = await createPolicy(page, token, `E2E OrgDef ${stamp}`, [
      { delaySeconds: 0, targets: [{ type: "all_admins" }] },
    ]);
    await setOrgDefault(page, token, orgPolicy.uid);
    const groupPolicy = await createPolicy(
      page,
      token,
      `E2E GroupPolicy ${stamp}`,
      [{ delaySeconds: 0, targets: [{ type: "all_admins" }] }],
    );
    const group = await createGroup(page, token, `E2E EscGroup ${stamp}`);
    const check = await createCheck(
      page,
      token,
      `E2E EscCheck ${stamp}`,
      group.uid,
    );

    try {
      // No indicator before any group-level policy is assigned.
      await page.goto("orgs/test/checks");
      await page.waitForLoadState("networkidle");
      const groupSection = page
        .getByTestId("group-section")
        .filter({ has: page.getByTestId("group-name").getByText(group.name) });
      await expect(groupSection).toBeVisible({ timeout: 10000 });
      await expect(
        groupSection.getByTestId("group-escalation-indicator"),
      ).not.toBeVisible();

      // Assign the group policy via the dedicated edit route.
      await groupSection.getByTestId("group-edit-button").click();
      await page.waitForURL(/\/check-groups\/[0-9a-f-]+\/edit$/);
      await page.waitForLoadState("networkidle");

      const select = page.getByTestId("escalation-policy-select");
      await expect(select).toBeVisible();
      // Inherit resolves straight to the org default for a group (shorter
      // chain than a check's).
      await expect(select).toContainText(orgPolicy.name);

      await select.click();
      await page.getByRole("option", { name: groupPolicy.name }).click();

      const patchPromise = page.waitForResponse(
        (resp) =>
          resp.url().includes(`/api/v1/orgs/test/check-groups/${group.uid}`) &&
          resp.request().method() === "PATCH",
      );
      await page.getByTestId("group-edit-submit").click();
      const patch = await patchPromise;
      expect(patch.status()).toBe(200);
      const patched = await patch.json();
      expect(patched.escalationPolicyUid).toBe(groupPolicy.uid);

      await page.waitForURL(/\/checks$/);
      await page.waitForLoadState("networkidle");

      // Group header indicator now appears.
      await expect(
        groupSection.getByTestId("group-escalation-indicator"),
      ).toBeVisible({ timeout: 10000 });

      // The check form's inherit label reflects the group's new policy.
      await page.goto(`orgs/test/checks/${check.uid}/edit`);
      await page.waitForLoadState("networkidle");
      const checkSelect = page.getByTestId("escalation-policy-select");
      await expect(checkSelect).toContainText(groupPolicy.name);
    } finally {
      await deleteGroup(page, token, group.uid);
      await clearOrgDefault(page, token);
      await deleteAllPolicies(page, token);
    }
  });

  test("clearing back to inherit restores the org default, and the description round-trips", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();

    const orgPolicy = await createPolicy(page, token, `E2E OrgDef2 ${stamp}`, [
      { delaySeconds: 0, targets: [{ type: "all_admins" }] },
    ]);
    await setOrgDefault(page, token, orgPolicy.uid);
    const groupPolicy = await createPolicy(
      page,
      token,
      `E2E GroupPolicy2 ${stamp}`,
      [{ delaySeconds: 0, targets: [{ type: "all_admins" }] }],
    );
    const group = await createGroup(page, token, `E2E ClearGroup ${stamp}`);
    // Pre-assign the group's policy directly via API.
    await page.request.patch(
      `${API_BASE}/api/v1/orgs/test/check-groups/${group.uid}`,
      { ...auth(token), data: { escalationPolicyUid: groupPolicy.uid } },
    );
    const check = await createCheck(
      page,
      token,
      `E2E ClearCheck ${stamp}`,
      group.uid,
    );

    try {
      await page.goto(`orgs/test/check-groups/${group.uid}/edit`);
      await page.waitForLoadState("networkidle");

      // Description round-trips.
      const description = `E2E description ${stamp}`;
      await page
        .getByTestId("group-edit-description-input")
        .fill(description);

      // Clear the escalation policy back to inherit.
      const select = page.getByTestId("escalation-policy-select");
      await select.click();
      await page.getByTestId("escalation-option-inherit").click();

      const patchPromise = page.waitForResponse(
        (resp) =>
          resp.url().includes(`/api/v1/orgs/test/check-groups/${group.uid}`) &&
          resp.request().method() === "PATCH",
      );
      await page.getByTestId("group-edit-submit").click();
      const patch = await patchPromise;
      const patched = await patch.json();
      expect(patched.escalationPolicyUid ?? null).toBeFalsy();
      expect(patched.description).toBe(description);

      // Verify server-side.
      const fetched = await getGroup(page, token, group.uid);
      expect(fetched.escalationPolicyUid ?? null).toBeFalsy();
      expect(fetched.description).toBe(description);

      await page.waitForURL(/\/checks$/);
      await page.waitForLoadState("networkidle");
      const groupSection = page
        .getByTestId("group-section")
        .filter({ has: page.getByTestId("group-name").getByText(group.name) });
      await expect(
        groupSection.getByTestId("group-escalation-indicator"),
      ).not.toBeVisible();

      // The check form's inherit label falls back to the org default again.
      await page.goto(`orgs/test/checks/${check.uid}/edit`);
      await page.waitForLoadState("networkidle");
      const checkSelect = page.getByTestId("escalation-policy-select");
      await expect(checkSelect).toContainText(orgPolicy.name);
    } finally {
      await deleteGroup(page, token, group.uid);
      await clearOrgDefault(page, token);
      await deleteAllPolicies(page, token);
    }
  });
});
