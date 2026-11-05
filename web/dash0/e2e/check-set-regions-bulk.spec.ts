import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-11-05-01: the checks list's bulk "Set regions…" pins the checks
// matched by the current filters to a chosen region set.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createCheck(page: Page, token: string, name: string): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, type: "http", config: { url: "https://example.com/bulk-regions" }, regions: ["default"] },
  });
  expect(resp.ok()).toBeTruthy();
  return ((await resp.json()) as { uid: string }).uid;
}

async function getCheck(page: Page, token: string, uid: string): Promise<{ regions: string[] }> {
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return resp.json();
}

test.describe("Bulk set regions", () => {
  test("pins the filtered checks to one region and leaves the others", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const slug = `bulk${stamp}`;
    const marker = `BulkRegions${stamp}`;

    const regionResp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug, name: `Bulk Region ${slug}` },
    });
    expect(regionResp.ok()).toBeTruthy();

    const first = await createCheck(page, token, `${marker} A`);
    const second = await createCheck(page, token, `${marker} B`);
    const third = await createCheck(page, token, `E2E Unrelated ${stamp}`);

    try {
      await page.goto(`orgs/test/checks?q=${marker}`);
      await page.getByTestId("checks-more-actions").click();
      await page.getByTestId("set-regions-button").click();

      await expect(page.getByTestId("set-regions-dialog")).toBeVisible();
      await expect(page.getByTestId("set-regions-scope")).toContainText("2 checks");

      await page.getByTestId(`set-regions-option-@${slug}`).click();
      await expect(page.getByTestId("set-regions-preview")).toContainText("2 checks will be pinned");

      await page.getByTestId("set-regions-confirm").click();
      await expect(page.getByTestId("set-regions-dialog")).toBeHidden();

      expect((await getCheck(page, token, first)).regions).toEqual([`@${slug}`]);
      expect((await getCheck(page, token, second)).regions).toEqual([`@${slug}`]);
      expect((await getCheck(page, token, third)).regions).toEqual(["default"]);
    } finally {
      for (const uid of [first, second, third]) {
        await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
      }
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });
});
