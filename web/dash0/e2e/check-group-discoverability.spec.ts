import { test, expect, API_BASE, uniqueStamp, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Spec 2026-09-16-13. The check form used to hide its Group field entirely
// while an organization had no groups (`showGroup`), so a new user never met
// the feature: no groups, no field, nothing to discover. The shared `test` org
// always HAS groups, which is exactly why nothing caught it — every test here
// therefore seeds a throwaway organization that genuinely has zero.

interface SeededOrg {
  slug: string;
  accessToken: string;
  refreshToken?: string;
  expiresIn?: number;
}

async function seedEmptyOrg(page: Page): Promise<SeededOrg> {
  const stamp = uniqueStamp();
  const email = `grp-disc-${stamp}@unknown.example`;
  const password = "Strong-Pass-123!";

  const createUserResp = await page.request.post(
    `${API_BASE}/api/v1/test/users`,
    { data: { email, password, name: "Group Discoverability User" } },
  );
  if (createUserResp.status() !== 201) {
    test.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
    );
  }

  const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email, password },
  });
  expect(loginResp.status()).toBe(200);
  const login = (await loginResp.json()) as { accessToken: string };

  const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${login.accessToken}` },
    data: { name: `Group Disc Co ${stamp}`, slug: `gd-${stamp}` },
  });
  expect(createOrgResp.status()).toBe(201);

  return (await createOrgResp.json()) as SeededOrg;
}

async function seedBrowserSession(page: Page, session: SeededOrg) {
  await page.addInitScript(
    ({ accessToken, refreshToken, expiresIn, slug }) => {
      localStorage.setItem("solidping_session_token", accessToken as string);
      if (refreshToken) {
        localStorage.setItem("solidping_refresh_token", refreshToken as string);
      }
      if (expiresIn) {
        localStorage.setItem(
          "solidping_expires_at",
          String(Date.now() + Number(expiresIn) * 1000),
        );
        localStorage.setItem("solidping_expires_in", String(expiresIn));
      }
      localStorage.setItem("solidping_org", slug as string);
    },
    {
      accessToken: session.accessToken,
      refreshToken: session.refreshToken ?? "",
      expiresIn: session.expiresIn ?? 0,
      slug: session.slug,
    },
  );
}

async function createCheckViaApi(page: Page, org: SeededOrg, name: string) {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/${org.slug}/checks`,
    {
      headers: { Authorization: `Bearer ${org.accessToken}` },
      data: {
        type: "http",
        name,
        config: { url: `https://acme.com/${uniqueStamp()}` },
        period: "00:05:00",
      },
    },
  );
  expect(resp.status()).toBe(201);

  return (await resp.json()) as { uid: string };
}

// The org has no groups, so the list endpoint must genuinely answer an empty
// array — asserted rather than assumed, since every expectation below is only
// meaningful for an organization in that state.
async function expectNoGroups(page: Page, org: SeededOrg) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/${org.slug}/check-groups`,
    { headers: { Authorization: `Bearer ${org.accessToken}` } },
  );
  expect(resp.status()).toBe(200);
  const body = (await resp.json()) as { data: unknown[] };
  expect(body.data).toEqual([]);
}

async function fillNewCheckForm(page: Page, name: string) {
  await expect(page.getByTestId("check-name-input")).toBeVisible({
    timeout: 15000,
  });
  await page.getByTestId("check-name-input").fill(name);
  await page.getByTestId("check-url-input").fill(`https://acme.com/${uniqueStamp()}`);
}

test.describe("Check group discoverability", () => {
  test("the check form still renders its group field in an org with zero groups", async ({
    page,
  }) => {
    const org = await seedEmptyOrg(page);
    await seedBrowserSession(page, org);
    await expectNoGroups(page, org);

    await page.goto(`orgs/${org.slug}/checks/new`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible({
      timeout: 15000,
    });

    await expandSection(page, "section-organization-trigger");

    // The regression: all three of these rendered nothing before the guard was
    // removed, so an org with no groups could not learn groups exist.
    await expect(page.getByTestId("check-group-field")).toBeVisible();
    await expect(page.getByTestId("check-group-select")).toBeVisible();
    await expect(page.getByTestId("check-group-empty-hint")).toBeVisible();
    await expect(page.getByTestId("check-form-new-group-button")).toBeVisible();
  });

  test("a group created inline from the check form is selected and saved on the check", async ({
    page,
  }) => {
    const org = await seedEmptyOrg(page);
    await seedBrowserSession(page, org);
    await expectNoGroups(page, org);

    await page.goto(`orgs/${org.slug}/checks/new`);
    await page.waitForLoadState("networkidle");

    const checkName = `Inline Group Check ${uniqueStamp()}`;
    await fillNewCheckForm(page, checkName);

    await expandSection(page, "section-organization-trigger");
    await page.getByTestId("check-form-new-group-button").click();

    const groupName = `Inline Group ${uniqueStamp()}`;
    await page.getByTestId("inline-new-group-name-input").fill(groupName);
    await page.getByTestId("inline-new-group-submit").click();

    // Creating it also selects it — the point of creating one from here.
    await expect(page.getByTestId("check-group-select")).toContainText(groupName, {
      timeout: 15000,
    });
    // ...and the empty-state hint is gone now that a group exists.
    await expect(page.getByTestId("check-group-empty-hint")).toHaveCount(0);
    // The rendered label alone would not catch a Radix Select that answers its
    // own reset ("") over the fresh selection — the field mirrors the state.
    await expect(page.getByTestId("check-group-field")).not.toHaveAttribute(
      "data-group-uid",
      "",
    );

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });

    const checkUid = page.url().match(/checks\/([0-9a-f-]+)/)?.[1];
    expect(checkUid).toBeTruthy();
    const checkResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${org.slug}/checks/${checkUid}`,
      { headers: { Authorization: `Bearer ${org.accessToken}` } },
    );
    const checkData = (await checkResp.json()) as { checkGroupUid?: string };
    expect(checkData.checkGroupUid).toBeTruthy();
  });

  test("the checks list explains groups when there are checks but none, and a group made there takes a check", async ({
    page,
  }) => {
    const org = await seedEmptyOrg(page);
    await seedBrowserSession(page, org);
    await createCheckViaApi(page, org, `Ungrouped Check ${uniqueStamp()}`);
    await expectNoGroups(page, org);

    await page.goto(`orgs/${org.slug}/checks`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("no-groups-empty-state")).toBeVisible({
      timeout: 15000,
    });

    // Create a group from the checks list, where the empty state just said
    // what one is.
    await page.getByTestId("new-group-button").click();
    const groupName = `List Group ${uniqueStamp()}`;
    await page.getByTestId("new-group-name-input").fill(groupName);
    await page.getByTestId("new-group-submit").click();

    // The explanation retires itself once the org has a group.
    await expect(page.getByTestId("no-groups-empty-state")).toHaveCount(0, {
      timeout: 15000,
    });

    // Same flow, continued: a check can now be put in it.
    await page.goto(`orgs/${org.slug}/checks/new`);
    await page.waitForLoadState("networkidle");

    const checkName = `Grouped Check ${uniqueStamp()}`;
    await fillNewCheckForm(page, checkName);

    await expandSection(page, "section-organization-trigger");
    const groupSelect = page.getByTestId("check-group-select");
    await expect(groupSelect).toBeVisible();
    await groupSelect.click();
    await page.getByRole("option", { name: groupName }).click();
    await expect(groupSelect).toContainText(groupName, { timeout: 10000 });

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });

    const checkUid = page.url().match(/checks\/([0-9a-f-]+)/)?.[1];
    const checkResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${org.slug}/checks/${checkUid}`,
      { headers: { Authorization: `Bearer ${org.accessToken}` } },
    );
    const checkData = (await checkResp.json()) as { checkGroupUid?: string };
    expect(checkData.checkGroupUid).toBeTruthy();

    const groupsResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/${org.slug}/check-groups`,
      { headers: { Authorization: `Bearer ${org.accessToken}` } },
    );
    const groupsBody = (await groupsResp.json()) as {
      data: { uid: string; name: string }[];
    };
    const created = groupsBody.data.find((g) => g.name === groupName);
    expect(created).toBeTruthy();
    expect(checkData.checkGroupUid).toBe(created?.uid);
  });
});
