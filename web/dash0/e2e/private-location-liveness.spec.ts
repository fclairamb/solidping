import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-09-25-05: every private location owns a liveness monitor, and the
// Private Locations page shows the location's state and links to it.

const SLUG = "e2e-live";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteRegion(page: Page, token: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${SLUG}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Private location liveness monitor", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    await deleteRegion(authenticatedPage, await getAuthToken(authenticatedPage));
  });

  test.afterEach(async ({ authenticatedPage }) => {
    await deleteRegion(authenticatedPage, await getAuthToken(authenticatedPage));
  });

  test("a new location gets a monitor, which can be turned off and back on", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };

    const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers,
      data: { slug: SLUG, name: "E2E Live" },
    });
    expect(created.ok()).toBeTruthy();
    const region = await created.json();
    expect(region.livenessMonitor?.uid).toBeTruthy();

    // The monitor is an ordinary check of the org.
    const check = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks/${region.livenessMonitor.uid}`,
      { headers },
    );
    expect(check.ok()).toBeTruthy();
    const checkBody = await check.json();
    expect(checkBody.type).toBe("private-location");
    expect(checkBody.name).toBe("Private location: E2E Live");
    expect(checkBody.config).toEqual({ region: `@${SLUG}` });

    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");

    // No agent enrolled yet: the location reads "No agent", the monitor is linked.
    await expect(page.getByTestId(`private-region-state-${SLUG}`)).toHaveAttribute("data-state", "empty");
    await expect(page.getByTestId(`private-region-online-${SLUG}`)).toContainText("0/0");
    const link = page.getByTestId(`liveness-monitor-link-${SLUG}`);
    await expect(link).toBeVisible();

    // Deleting the monitor is remembered: the page offers the one-click re-enable.
    const deleted = await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/checks/${region.livenessMonitor.uid}`,
      { headers },
    );
    expect(deleted.ok()).toBeTruthy();

    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId(`liveness-monitor-off-${SLUG}`)).toBeVisible();

    await page.getByTestId(`enable-liveness-monitor-${SLUG}`).click();
    await expect(page.getByTestId(`liveness-monitor-link-${SLUG}`)).toBeVisible();

    // The link opens the monitor's check page.
    await page.getByTestId(`liveness-monitor-link-${SLUG}`).click();
    await expect(page).toHaveURL(/\/checks\/[0-9a-f-]{36}/);
    await expect(page.getByText("Private location: E2E Live").first()).toBeVisible();
  });
});
