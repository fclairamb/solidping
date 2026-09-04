import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-07-21-01: the optional "Region Spread" override on
// multi-region checks (backend field shipped by spec 2026-07-20-05, exposed
// here in the create/edit form for the first time).
//
// The local test server only has the single "default" region unless
// SP_REGIONS is seeded, so these tests create a throwaway private region via
// the lightweight `POST /private-regions` endpoint (the same mechanism
// check-ssh-tunnel.spec.ts already uses) purely to get a real second region
// to select — no agent enrollment is needed since these checks are never run.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createPrivateRegion(
  page: Page,
  token: string,
  slug: string,
): Promise<void> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/private-regions`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug, name: `Spread Region ${slug}` },
    },
  );
  expect(resp.ok()).toBeTruthy();
}

async function deletePrivateRegion(
  page: Page,
  token: string,
  slug: string,
): Promise<void> {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/private-regions/${slug}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
}

// ensureRegionChecked clicks the region checkbox only if it isn't already in
// the desired state — clicking an already-checked Radix checkbox again would
// uncheck it, and the pre-selected region set isn't a hard-coded assumption
// this file should make.
async function ensureRegionChecked(page: Page, regionTestId: string): Promise<void> {
  const option = page.getByTestId(regionTestId);
  const checkbox = option.getByRole("checkbox");
  if (!(await checkbox.isChecked())) {
    await option.click();
  }
}

async function openNewCheckForm(page: Page): Promise<void> {
  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  // Pin the interval to 1 minute so the automatic default (period / regions)
  // and the out-of-range boundary are both deterministic across environments.
  await page.getByTestId("check-period-select").click();
  await page.getByRole("option", { name: "1 minute" }).click();
}

test.describe("Check form — Region Spread", () => {
  test("is absent with a single region and appears once a second region is selected", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const regionSlug = `vis-${Date.now().toString(36)}`.slice(0, 20);
    await createPrivateRegion(page, token, regionSlug);

    await openNewCheckForm(page);
    await page.getByTestId("check-name-input").fill(`E2E Region Spread Visibility ${Date.now()}`);
    await page.getByTestId("check-url-input").fill("https://example.com/region-spread-visibility");

    // Only "default" selected (the org's single default region) — the field
    // must not render with just one region picked.
    await ensureRegionChecked(page, "region-option-default");
    await expect(page.getByTestId(`region-option-@${regionSlug}`).getByRole("checkbox")).not.toBeChecked();
    await expect(page.getByTestId("check-region-spread-input")).toHaveCount(0);

    // Selecting the second region reveals it.
    await ensureRegionChecked(page, `region-option-@${regionSlug}`);
    await expect(page.getByTestId("check-region-spread-input")).toBeVisible();

    // Deselecting back down to one region hides it again.
    await page.getByTestId(`region-option-@${regionSlug}`).click();
    await expect(page.getByTestId("check-region-spread-input")).toHaveCount(0);

    await deletePrivateRegion(page, token, regionSlug);
  });

  test("a custom spread persists across save and reload, and clearing it restores the automatic default", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const regionSlug = `persist-${Date.now().toString(36)}`.slice(0, 20);
    await createPrivateRegion(page, token, regionSlug);

    await openNewCheckForm(page);
    const checkName = `E2E Region Spread Persist ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/region-spread-persist");

    await ensureRegionChecked(page, "region-option-default");
    await ensureRegionChecked(page, `region-option-@${regionSlug}`);

    // 2 regions over a 1-minute period -> automatic default is 30s.
    await expect(page.getByTestId("region-spread-help")).toContainText("30 s");

    await page.getByTestId("check-region-spread-input").fill("5");
    await expect(page.getByTestId("region-spread-error")).toHaveCount(0);
    await expect(page.getByTestId("regions-period-hint")).toContainText("5 s");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Re-open the edit form and confirm the override round-tripped through a
    // real save, not just held in client state.
    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/edit$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-region-spread-input")).toHaveValue("5");
    await expect(page.getByTestId("region-spread-help")).toContainText(
      "Overrides how far apart regions are staggered",
    );

    // Clear it back to automatic and save again.
    await page.getByTestId("check-region-spread-input").fill("");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/edit$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-region-spread-input")).toHaveValue("");
    await expect(page.getByTestId("region-spread-help")).toContainText("Automatic: 30 s");

    await deletePrivateRegion(page, token, regionSlug);
  });

  test("an out-of-range value shows an inline validation error and blocks save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const regionSlug = `range-${Date.now().toString(36)}`.slice(0, 20);
    await createPrivateRegion(page, token, regionSlug);

    await openNewCheckForm(page);
    await page.getByTestId("check-name-input").fill(`E2E Region Spread Range ${Date.now()}`);
    await page.getByTestId("check-url-input").fill("https://example.com/region-spread-range");

    await ensureRegionChecked(page, "region-option-default");
    await ensureRegionChecked(page, `region-option-@${regionSlug}`);

    // Period is pinned to 1 minute (60s); a spread equal to the period is out
    // of range (must be strictly less).
    await page.getByTestId("check-region-spread-input").fill("60");
    await expect(page.getByTestId("region-spread-error")).toContainText(
      "less than the check period",
    );

    await page.getByTestId("check-submit-button").click();
    // Blocked client-side — still on the create form, never navigated away.
    await expect(page).toHaveURL(/\/checks\/new/);
    await expect(page.getByTestId("region-spread-error")).toBeVisible();

    await deletePrivateRegion(page, token, regionSlug);
  });
});
