import { test, expect, API_BASE, type Page } from "./fixtures";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function setMaxChecks(
  page: Page,
  token: string,
  maxChecks: number | null,
): Promise<void> {
  const resp = await page.request.patch(
    `${API_BASE}/api/v1/orgs/test/entitlements`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { limits: { maxChecks } },
    },
  );
  expect(resp.ok()).toBeTruthy();
}

// Reset all limits to unlimited. PATCH with maxChecks:null is a no-op (null is
// ignored by the partial merge), so a full-replace PUT is the only reliable way
// to clear a previously-set cap — otherwise a stale maxChecks leaks into later
// suites and 402s their check creation.
async function resetEntitlements(page: Page, token: string): Promise<void> {
  const resp = await page.request.put(
    `${API_BASE}/api/v1/orgs/test/entitlements`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { limits: {} },
    },
  );
  expect(resp.ok()).toBeTruthy();
}

// These tests cover the entitlements usage surface: the new
// /organization/usage page (limits vs. usage bars) and the MaxChecks
// quota enforced at check creation (402 QUOTA_EXCEEDED).
test.describe("Entitlements usage", () => {
  test.afterEach(async ({ authenticatedPage }) => {
    // Always restore unlimited checks so other suites are unaffected.
    const token = await getAuthToken(authenticatedPage);
    await resetEntitlements(authenticatedPage, token);
  });

  test("usage page renders seven limit/usage rows", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const token = await getAuthToken(page);
    await resetEntitlements(page, token);

    await page.goto("orgs/test/organization/usage");
    await page.waitForLoadState("networkidle");

    // Heading + the seven rows (Checks, Checks per minute, Users, Private
    // location agents, Service level objectives, Custom domains, WhatsApp
    // messages this month).
    await expect(
      page.getByRole("heading", { name: /usage/i }),
    ).toBeVisible();
    await expect(page.getByTestId(/^usage-row-/)).toHaveCount(7);
    await expect(
      page.getByTestId("usage-row-Private location agents"),
    ).toBeVisible();
    await expect(
      page.getByTestId("usage-row-Custom domains"),
    ).toBeVisible();
    await expect(
      page.getByTestId("usage-row-WhatsApp messages this month"),
    ).toBeVisible();
    await expect(
      page.getByTestId("usage-row-Service level objectives"),
    ).toBeVisible();

    // With null limits, each row shows the "Unlimited" label.
    await expect(page.getByText(/unlimited/i).first()).toBeVisible();
  });

  test("a maxCustomDomains cap of 0 renders as 0 / 0, not Unlimited", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Free-tier semantics: the cap is 0, not absent — it must render as a
    // real "0 / 0" saturated row, never fall through to the unlimited label.
    const resp = await page.request.patch(
      `${API_BASE}/api/v1/orgs/test/entitlements`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { limits: { maxCustomDomains: 0 } },
      },
    );
    expect(resp.ok()).toBeTruthy();

    await page.goto("orgs/test/organization/usage");
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("usage-row-Custom domains");
    await expect(row).toBeVisible();
    await expect(row).toContainText("0 / 0");
    await expect(row).not.toContainText(/unlimited/i);
  });

  // Spec 2026-08-26-03: an org over its per-minute execution cap used to learn
  // it from a support ticket — the only traces were an INFO log and a
  // Prometheus counter. The banner is the customer-visible half.
  //
  // The cap is set just BELOW the org's real demand rather than to 0: that is
  // enough to make the banner render, while keeping the actual skip rate (and
  // so the `skippedToday` residue this shared org carries into later suites)
  // as close to nothing as the fixture allows.
  test("an org over its per-minute cap is told so on the checks list and the usage page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const entResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/entitlements`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(entResp.ok()).toBeTruthy();
    const demand = (await entResp.json()).checksPerMinute.demand as number;

    // With no scheduled demand at all there is nothing to be over, and the
    // banner is correctly silent — nothing to assert.
    test.skip(demand < 1, `test org schedules ${demand}/min, too little to exceed a cap`);

    const capResp = await page.request.patch(
      `${API_BASE}/api/v1/orgs/test/entitlements`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { limits: { maxChecksPerMinute: Math.max(0, Math.floor(demand) - 1) } },
      },
    );
    expect(capResp.ok()).toBeTruthy();

    // Surface 1: the checks list, where the unexplained gaps actually show up.
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const listBanner = page.getByTestId("check-rate-limit-banner");
    await expect(listBanner).toBeVisible();
    await expect(listBanner).toContainText(/skipped/i);
    // It must link somewhere actionable, not just complain.
    await expect(
      page.getByTestId("check-rate-limit-usage-link"),
    ).toBeVisible();

    // Surface 2: the usage page, next to the bar it explains.
    await page.goto("orgs/test/organization/usage");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("check-rate-limit-banner")).toBeVisible();
    // No self-link on the page the reader is already on.
    await expect(
      page.getByTestId("check-rate-limit-usage-link"),
    ).toHaveCount(0);
  });

  test("creating a check over the maxChecks cap returns 402 QUOTA_EXCEEDED", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Count current non-internal checks so we can set the cap at the current
    // usage, guaranteeing the next create is over the limit regardless of
    // how many checks already exist in the shared test org.
    const usageResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/entitlements?with=usage`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(usageResp.ok()).toBeTruthy();
    const current = (await usageResp.json()).usage.checks as number;

    await setMaxChecks(page, token, current);

    // Next create is at/over the cap → 402 with QUOTA_EXCEEDED.
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { type: "http", config: { url: `https://example.com/${Date.now()}` } },
      },
    );
    expect(createResp.status()).toBe(402);
    const body = await createResp.json();
    expect(body.code).toBe("QUOTA_EXCEEDED");
    expect(body.limitName).toBe("MaxChecks");

    // The usage page reflects the saturated checks bar (current / current).
    await page.goto("orgs/test/organization/usage");
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId(new RegExp(`^usage-row-`)).first(),
    ).toBeVisible();
  });
});
