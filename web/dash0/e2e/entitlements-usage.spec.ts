import { test, expect, type Page } from "./fixtures";

const API_BASE = "http://localhost:4000";

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

// These tests cover the entitlements usage surface: the new
// /organization/usage page (limits vs. usage bars) and the MaxChecks
// quota enforced at check creation (402 QUOTA_EXCEEDED).
test.describe("Entitlements usage", () => {
  test.afterEach(async ({ authenticatedPage }) => {
    // Always restore unlimited checks so other suites are unaffected.
    const token = await getAuthToken(authenticatedPage);
    await setMaxChecks(authenticatedPage, token, null);
  });

  test("usage page renders three limit/usage rows", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const token = await getAuthToken(page);
    await setMaxChecks(page, token, null);

    await page.goto("orgs/test/organization/usage");
    await page.waitForLoadState("networkidle");

    // Heading + the three rows (Checks, Checks per minute, SSO users).
    await expect(
      page.getByRole("heading", { name: /usage/i }),
    ).toBeVisible();
    await expect(page.getByTestId(/^usage-row-/)).toHaveCount(3);

    // With null limits, each row shows the "Unlimited" label.
    await expect(page.getByText(/unlimited/i).first()).toBeVisible();
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
