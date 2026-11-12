import { test, expect, API_BASE, uniqueStamp, type Page } from "./fixtures";
import { checkRowLocator } from "./locators";

// Group filter on the checks list: a "Group" facet, a clickable group name in
// each header, and a ?group=<slug|none> URL param that combines (AND) with the
// other filters. Seeds a throwaway org so only these checks exist.

interface SeededOrg {
  slug: string;
  accessToken: string;
  refreshToken?: string;
  expiresIn?: number;
}

async function seedOrg(page: Page): Promise<SeededOrg> {
  const stamp = uniqueStamp();
  const email = `grp-filter-${stamp}@unknown.example`;
  const password = "Strong-Pass-123!";
  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password, name: "Group Filter User" },
  });
  if (created.status() !== 201) {
    test.skip(true, `test user-seed endpoint unavailable: ${created.status()}`);
  }
  const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email, password },
  });
  expect(login.status()).toBe(200);
  const { accessToken } = (await login.json()) as { accessToken: string };
  const org = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { name: `Group Filter Co ${stamp}`, slug: `gf-${stamp}` },
  });
  expect(org.status()).toBe(201);
  return (await org.json()) as SeededOrg;
}

async function seedBrowserSession(page: Page, s: SeededOrg) {
  await page.addInitScript(
    ({ accessToken, refreshToken, expiresIn, slug }) => {
      localStorage.setItem("solidping_session_token", accessToken as string);
      if (refreshToken) localStorage.setItem("solidping_refresh_token", refreshToken as string);
      if (expiresIn) {
        localStorage.setItem("solidping_expires_at", String(Date.now() + Number(expiresIn) * 1000));
        localStorage.setItem("solidping_expires_in", String(expiresIn));
      }
      localStorage.setItem("solidping_org", slug as string);
    },
    {
      accessToken: s.accessToken,
      refreshToken: s.refreshToken ?? "",
      expiresIn: s.expiresIn ?? 0,
      slug: s.slug,
    },
  );
}

async function createGroup(page: Page, org: SeededOrg, name: string, slug: string) {
  const r = await page.request.post(`${API_BASE}/api/v1/orgs/${org.slug}/check-groups`, {
    headers: { Authorization: `Bearer ${org.accessToken}` },
    data: { name, slug },
  });
  expect(r.status()).toBe(201);
  return (await r.json()) as { uid: string };
}

async function createCheck(page: Page, org: SeededOrg, name: string, checkGroupUid?: string) {
  const r = await page.request.post(`${API_BASE}/api/v1/orgs/${org.slug}/checks`, {
    headers: { Authorization: `Bearer ${org.accessToken}` },
    data: {
      type: "http",
      name,
      config: { url: `https://acme.com/${uniqueStamp()}` },
      period: "00:05:00",
      ...(checkGroupUid ? { checkGroupUid } : {}),
    },
  });
  expect(r.status()).toBe(201);
}

test.describe("Checks list group filter", () => {
  let org: SeededOrg;
  let names: { alpha: string; beta: string; loose: string };

  test.beforeEach(async ({ page }) => {
    org = await seedOrg(page);
    const stamp = uniqueStamp();
    names = { alpha: `alpha-check-${stamp}`, beta: `beta-check-${stamp}`, loose: `loose-check-${stamp}` };
    const alpha = await createGroup(page, org, "Alpha Group", "alpha-group");
    const beta = await createGroup(page, org, "Beta Group", "beta-group");
    await createCheck(page, org, names.alpha, alpha.uid);
    await createCheck(page, org, names.beta, beta.uid);
    await createCheck(page, org, names.loose);
    await seedBrowserSession(page, org);
    await page.goto(`orgs/${org.slug}/checks`);
    // Positive control: everything is visible before any filtering.
    for (const n of Object.values(names)) {
      await expect(checkRowLocator(page, n)).toBeVisible({ timeout: 15000 });
    }
  });

  test("picking a group in the facet shows only its checks", async ({ page }) => {
    await page.getByTestId("group-filter").click();
    await page.getByTestId("group-filter-option-alpha-group").click();
    await expect(page).toHaveURL(/group=alpha-group/);
    await expect(checkRowLocator(page, names.alpha)).toBeVisible();
    await expect(checkRowLocator(page, names.beta)).toHaveCount(0);
    await expect(checkRowLocator(page, names.loose)).toHaveCount(0);
  });

  test("clicking a group header name filters on that group", async ({ page }) => {
    await page
      .getByTestId("group-filter-link")
      .filter({ has: page.locator('[data-testid="group-name"][data-group-name="Beta Group"]') })
      .click();
    await expect(page).toHaveURL(/group=beta-group/);
    await expect(checkRowLocator(page, names.beta)).toBeVisible();
    await expect(checkRowLocator(page, names.alpha)).toHaveCount(0);
    await expect(checkRowLocator(page, names.loose)).toHaveCount(0);
  });

  test("status combines with the group filter", async ({ page }) => {
    const stamp = uniqueStamp();
    const alphaGroup = await createGroup(page, org, "Alpha Two", "alpha-two");
    const betaGroup = await createGroup(page, org, "Beta Two", "beta-two");
    const downIn = `down-in-${stamp}`;
    const upIn = `up-in-${stamp}`;
    const downOut = `down-out-${stamp}`;
    await createCheck(page, org, downIn, alphaGroup.uid);
    await createCheck(page, org, upIn, alphaGroup.uid);
    await createCheck(page, org, downOut, betaGroup.uid);
    const wireStatus: Record<string, string> = { [downIn]: "down", [upIn]: "up", [downOut]: "down" };

    // Checks cannot be made down without a real probe, so the list response
    // is patched: fetch the server's list WITHOUT the status filter (the group
    // filter still applies server-side), stamp the statuses, then apply the
    // status filter ourselves.
    let sawBoth = false;
    await page.route(`**/api/v1/orgs/${org.slug}/checks?*`, async (route) => {
      const url = new URL(route.request().url());
      const wanted = url.searchParams.get("status");
      if (url.searchParams.get("checkGroupUid") && wanted === "down") sawBoth = true;
      url.searchParams.delete("status");
      const resp = await route.fetch({ url: url.toString() });
      const body = (await resp.json()) as { data: Array<{ name?: string; status?: string }> };
      body.data = body.data
        .map((c) => ({ ...c, status: wireStatus[c.name ?? ""] ?? c.status }))
        .filter((c) => !wanted || wanted.split(",").includes(c.status ?? ""));
      await route.fulfill({ response: resp, json: body });
    });

    await page.goto(`orgs/${org.slug}/checks?group=alpha-two&status=down`);
    await expect(checkRowLocator(page, downIn)).toBeVisible({ timeout: 15000 });
    expect(sawBoth).toBe(true);
    await expect(checkRowLocator(page, upIn)).toHaveCount(0);
    await expect(checkRowLocator(page, downOut)).toHaveCount(0);
    await expect(checkRowLocator(page, names.alpha)).toHaveCount(0);
  });

  test("reset clears the group filter", async ({ page }) => {
    await page.goto(`orgs/${org.slug}/checks?group=alpha-group`);
    await expect(checkRowLocator(page, names.alpha)).toBeVisible({ timeout: 15000 });
    await expect(checkRowLocator(page, names.beta)).toHaveCount(0);
    await page.getByTestId("reset-filters").click();
    await expect(page).not.toHaveURL(/group=/);
    await expect(checkRowLocator(page, names.beta)).toBeVisible();
    await expect(checkRowLocator(page, names.loose)).toBeVisible();
  });

  test("?group=none shows only ungrouped checks", async ({ page }) => {
    await page.goto(`orgs/${org.slug}/checks?group=none`);
    await expect(checkRowLocator(page, names.loose)).toBeVisible({ timeout: 15000 });
    await expect(checkRowLocator(page, names.alpha)).toHaveCount(0);
    await expect(checkRowLocator(page, names.beta)).toHaveCount(0);
  });

  test("an unknown group slug shows an empty state, not the whole list", async ({ page }) => {
    await page.goto(`orgs/${org.slug}/checks?group=no-such-group`);
    await expect(page.getByTestId("group-filter-unknown")).toBeVisible({ timeout: 15000 });
    await expect(checkRowLocator(page, names.alpha)).toHaveCount(0);
    await expect(checkRowLocator(page, names.loose)).toHaveCount(0);
    await page.getByTestId("group-filter-clear").click();
    await expect(checkRowLocator(page, names.alpha)).toBeVisible();
  });
});
