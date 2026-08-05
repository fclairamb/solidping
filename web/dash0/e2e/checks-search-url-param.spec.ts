import { test, expect, API_BASE, type Page } from "./fixtures";

// Covers spec 2026-08-04-02: the checks-list search box mirrors ?q= in the
// URL. The regression this guards against: a naive `validateSearch`-only
// implementation reads fine on client-side navigation but drops the param on
// a genuine cold load under the /orgs/$org layout route — so this spec
// exercises a full page reload (page.goto), not just in-app navigation.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createCheckViaApi(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      type: "http",
      name,
      config: { url: `https://example.com/${Date.now()}` },
      period: "00:05:00",
    },
  });
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function deleteCheckViaApi(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Checks search box syncs to ?q=", () => {
  test("typing updates the URL, and a reload keeps the input and filter", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now();
    const matchName = `SearchParamMatch-${suffix}`;
    const otherName = `SearchParamOther-${suffix}`;
    const matchCheck = await createCheckViaApi(page, token, matchName);
    const otherCheck = await createCheckViaApi(page, token, otherName);

    try {
      await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
      await page.waitForURL(/\/checks/);
      await page.waitForLoadState("networkidle");

      const searchInput = page.getByTestId("checks-search-input");
      await expect(searchInput).toBeVisible();

      // Before typing, both checks are visible and the URL has no q param.
      await expect(page.getByText(matchName)).toBeVisible();
      await expect(page.getByText(otherName)).toBeVisible();
      expect(new URL(page.url()).searchParams.get("q")).toBeNull();

      // Typing narrows the list and updates the URL (debounced, replace —
      // asserted by polling rather than requiring an exact history length).
      await searchInput.fill(matchName);
      await expect.poll(() => new URL(page.url()).searchParams.get("q")).toBe(matchName);
      await expect(page.getByText(matchName)).toBeVisible();
      await expect(page.getByText(otherName)).not.toBeVisible();

      // Cold reload (not client-side nav): the input stays pre-filled and the
      // list stays filtered from the URL alone.
      await page.reload();
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("checks-search-input")).toHaveValue(matchName);
      await expect(page.getByText(matchName)).toBeVisible();
      await expect(page.getByText(otherName)).not.toBeVisible();

      // Clearing the box removes the param entirely (not left as `q=`).
      await page.getByTestId("checks-search-input").fill("");
      await expect.poll(() => new URL(page.url()).searchParams.has("q")).toBe(false);
      await expect(page.getByText(otherName)).toBeVisible();
    } finally {
      await deleteCheckViaApi(page, token, matchCheck.uid);
      await deleteCheckViaApi(page, token, otherCheck.uid);
    }
  });

  test("cold deep-link with ?q= pre-fills the input and filters the list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now();
    const matchName = `DeepLinkMatch-${suffix}`;
    const otherName = `DeepLinkOther-${suffix}`;
    const matchCheck = await createCheckViaApi(page, token, matchName);
    const otherCheck = await createCheckViaApi(page, token, otherName);

    try {
      // Navigate to the checks list first (to resolve org/base path), then a
      // genuine cold load of the deep-linked URL.
      await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
      await page.waitForURL(/\/checks/);

      const target = new URL(page.url());
      target.searchParams.set("q", matchName);
      await page.goto(target.toString());
      await page.waitForLoadState("networkidle");

      await expect(page.getByTestId("checks-search-input")).toHaveValue(matchName);
      await expect(page.getByText(matchName)).toBeVisible();
      await expect(page.getByText(otherName)).not.toBeVisible();
    } finally {
      await deleteCheckViaApi(page, token, matchCheck.uid);
      await deleteCheckViaApi(page, token, otherCheck.uid);
    }
  });

  test("q survives changes to other params and vice versa", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now();
    const matchName = `GroupByMatch-${suffix}`;
    const matchCheck = await createCheckViaApi(page, token, matchName);

    try {
      await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
      await page.waitForURL(/\/checks/);
      await page.waitForLoadState("networkidle");

      const searchInput = page.getByTestId("checks-search-input");
      await searchInput.fill(matchName);
      await expect.poll(() => new URL(page.url()).searchParams.get("q")).toBe(matchName);

      // Switch the groupBy mode ("Host") — a different search param.
      await page.getByTestId("group-by-host").click();
      await expect.poll(() => new URL(page.url()).searchParams.get("groupBy")).toBe("host");
      // q must still be present after the groupBy write.
      expect(new URL(page.url()).searchParams.get("q")).toBe(matchName);

      // Now clear q and confirm groupBy survives.
      await searchInput.fill("");
      await expect.poll(() => new URL(page.url()).searchParams.has("q")).toBe(false);
      expect(new URL(page.url()).searchParams.get("groupBy")).toBe("host");
    } finally {
      await deleteCheckViaApi(page, token, matchCheck.uid);
    }
  });
});
