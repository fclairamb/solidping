import { test, expect, API_BASE, type Page } from "./fixtures";

// End-to-end coverage for the escalation assignment feature (spec
// 2026-07-19-01): the org-default escalation policy field (with blast-radius
// count), the check-form assignment picker (inherit label + "No escalation
// (silent)" shortcut), the policy-list "0 steps — silent" badge, and the
// delete-confirmation usage count.

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

async function createCheck(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    ...auth(token),
    data: { name, type: "http", config: { url: "https://example.com" } },
  });
  return resp.json();
}

async function getCheck(
  page: Page,
  token: string,
  uid: string,
): Promise<{ escalationPolicyUid?: string | null }> {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    auth(token),
  );
  return resp.json();
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

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    auth(token),
  );
}

test.describe("Escalation assignment", () => {
  test("org settings: setting a default escalation policy PATCHes it and shows the blast radius", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const policy = await createPolicy(page, token, `E2E Default ${stamp}`, [
      { delaySeconds: 0, targets: [{ type: "all_admins" }] },
    ]);

    try {
      await page.goto("orgs/test/organization/settings");
      await page.waitForLoadState("networkidle");

      await expect(
        page.getByRole("heading", { name: /default escalation policy/i }),
      ).toBeVisible();

      // Pick the policy in the Select.
      await page.getByTestId("default-escalation-select").click();
      await page.getByRole("option", { name: policy.name }).click();

      // Blast-radius line appears once a policy is chosen.
      await expect(page.getByTestId("escalation-blast-radius")).toBeVisible();
      await expect(page.getByTestId("escalation-blast-radius")).toContainText(
        /currently inherit/i,
      );

      const patchPromise = page.waitForResponse(
        (resp) =>
          resp.url().includes("/api/v1/orgs/test/settings") &&
          resp.request().method() === "PATCH",
      );
      await page.getByTestId("default-escalation-save").click();
      const patch = await patchPromise;
      expect(patch.status()).toBe(200);
      const body = await patch.json();
      expect(body.defaultEscalationPolicyUid).toBe(policy.uid);

      await expect(page.getByText(/settings saved/i)).toBeVisible();
    } finally {
      await clearOrgDefault(page, token);
      await deleteAllPolicies(page, token);
    }
  });

  test("policy list: a zero-step policy shows the silent badge", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    await createPolicy(page, token, `E2E Silent ${stamp}`, []); // zero steps

    try {
      await page.goto("orgs/test/escalation-policies");
      await page.waitForLoadState("networkidle");

      const row = page
        .getByTestId("policy-row")
        .filter({ hasText: `E2E Silent ${stamp}` });
      await expect(row.getByTestId("policy-silent-badge")).toBeVisible();
      await expect(row.getByTestId("policy-silent-badge")).toContainText(
        /silent/i,
      );
    } finally {
      await deleteAllPolicies(page, token);
    }
  });

  test("policy list: delete confirmation shows the usage count when a check references the policy", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const policy = await createPolicy(page, token, `E2E Used ${stamp}`, [
      { delaySeconds: 0, targets: [{ type: "all_admins" }] },
    ]);
    const check = await createCheck(page, token, `E2E Uses Policy ${stamp}`);
    // Assign the policy to the check via PATCH.
    await page.request.patch(
      `${API_BASE}/api/v1/orgs/test/checks/${check.uid}`,
      { ...auth(token), data: { escalationPolicyUid: policy.uid } },
    );

    try {
      await page.goto("orgs/test/escalation-policies");
      await page.waitForLoadState("networkidle");

      const row = page
        .getByTestId("policy-row")
        .filter({ hasText: `E2E Used ${stamp}` });
      await row.getByRole("button", { name: /delete/i }).click();

      const usage = page.getByTestId("policy-delete-usage");
      await expect(usage).toBeVisible();
      await expect(usage).toContainText(/used by 1 checks/i);
      await expect(usage).toContainText(/fall back to inherited escalation/i);
    } finally {
      await deleteCheck(page, token, check.uid);
      await deleteAllPolicies(page, token);
    }
  });

  test("check form: inherit label resolves to the org default, and the silent shortcut assigns a zero-step policy", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();

    // An org default so the inherit label is non-blind.
    const orgPolicy = await createPolicy(page, token, `E2E OrgDef ${stamp}`, [
      { delaySeconds: 0, targets: [{ type: "all_admins" }] },
    ]);
    await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
      ...auth(token),
      data: { defaultEscalationPolicyUid: orgPolicy.uid },
    });
    const check = await createCheck(page, token, `E2E Picker ${stamp}`);

    try {
      await page.goto(`orgs/test/checks/${check.uid}/edit`);
      await page.waitForLoadState("networkidle");

      const select = page.getByTestId("escalation-policy-select");
      await expect(select).toBeVisible();
      // Inherit label live-resolves to the org default policy name.
      await expect(select).toContainText(orgPolicy.name);

      // Use the "No escalation (silent)" shortcut → creates/assigns a zero-step
      // policy and shows the informational note.
      await select.click();
      await page.getByTestId("escalation-option-silent").click();
      await expect(page.getByTestId("escalation-silent-note")).toBeVisible();

      // Save the check and verify it now points at a zero-step policy.
      await page.getByTestId("check-submit-button").click();
      await page.waitForURL((url) => !url.pathname.endsWith("/edit"), {
        timeout: 10000,
      });

      const updated = await getCheck(page, token, check.uid);
      expect(updated.escalationPolicyUid).toBeTruthy();

      const policyResp = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/escalation-policies/${updated.escalationPolicyUid}`,
        auth(token),
      );
      const policyBody = await policyResp.json();
      expect((policyBody.steps ?? []).length).toBe(0);
    } finally {
      await clearOrgDefault(page, token);
      await deleteCheck(page, token, check.uid);
      await deleteAllPolicies(page, token);
    }
  });
});
