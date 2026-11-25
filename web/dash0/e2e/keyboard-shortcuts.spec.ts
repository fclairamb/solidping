import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-11-25-06: page-level keyboard shortcuts.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createCheck(page: Page, token: string, name: string): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, type: "http", config: { url: "https://example.com/kbd" }, regions: ["default"] },
  });
  expect(resp.ok()).toBeTruthy();
  return ((await resp.json()) as { uid: string }).uid;
}

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Keyboard shortcuts", () => {
  test("Esc resets the group facet and keeps q", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks?group=none&q=kbd");
    await expect(page.getByTestId("reset-filters")).toBeVisible();
    await page.locator("body").click({ position: { x: 1, y: 1 } });
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("reset-filters")).toBeHidden();
    await expect(page).not.toHaveURL(/group=/);
    await expect(page).toHaveURL(/q=kbd/);
  });

  test("Esc with status and labels facets clears both", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks?status=up&labels=env:prod");
    await expect(page.getByTestId("reset-filters")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("reset-filters")).toBeHidden();
    await expect(page).not.toHaveURL(/status=|labels=/);
  });

  test("Esc closes an open filter dropdown first and keeps the filters", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks?status=up");
    await page.getByTestId("type-filter").click();
    await expect(page.getByRole("menu").or(page.getByRole("dialog")).first()).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("menu")).toBeHidden();
    await expect(page).toHaveURL(/status=up/);
    await expect(page.getByTestId("reset-filters")).toBeVisible();
  });

  test("search box: Esc clears, then blurs, then resets facets", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks?status=up");
    const search = page.getByTestId("checks-search-input");
    await search.fill("zzz");
    await page.keyboard.press("Escape");
    await expect(search).toHaveValue("");
    await expect(search).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(search).not.toBeFocused();
    await expect(page.getByTestId("reset-filters")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("reset-filters")).toBeHidden();
  });

  test("typing guard: n and / are typed into an unrelated input", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks/new");
    const name = page.getByTestId("check-name-input");
    await name.click();
    await page.keyboard.type("n/x");
    await expect(name).toHaveValue("n/x");
    await expect(page).toHaveURL(/checks\/new/);
  });

  test("/ focuses the search box without typing it", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks");
    await page.locator("body").click({ position: { x: 1, y: 1 } });
    await page.keyboard.press("/");
    const search = page.getByTestId("checks-search-input");
    await expect(search).toBeFocused();
    await expect(search).toHaveValue("");
  });

  test("n opens the new check page", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks");
    await page.locator("body").click({ position: { x: 1, y: 1 } });
    await page.keyboard.press("n");
    await expect(page).toHaveURL(/checks\/new/);
  });

  test("Ctrl+Enter saves a valid edit form and not an invalid one", async ({ authenticatedPage: page }) => {
    const token = await getAuthToken(page);
    const name = `Kbd${Date.now()}`;
    const uid = await createCheck(page, token, name);
    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      const input = page.getByTestId("check-name-input");
      await expect(input).toHaveValue(name);

      // Invalid: empty name must not save.
      await input.fill("");
      await input.press("Control+Enter");
      await page.waitForTimeout(500);
      await expect(page).toHaveURL(/\/edit$/);
      const unchanged = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      expect((await unchanged.json()).name).toBe(name);

      // Valid: saves.
      await input.fill(`${name}-saved`);
      await input.press("Control+Enter");
      await expect
        .poll(async () => {
          const r = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
            headers: { Authorization: `Bearer ${token}` },
          });
          return (await r.json()).name;
        })
        .toBe(`${name}-saved`);
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("? opens the shortcuts dialog and Esc closes it without touching filters", async ({ authenticatedPage: page }) => {
    await page.goto("orgs/test/checks?status=up");
    await page.locator("body").click({ position: { x: 1, y: 1 } });
    await page.keyboard.press("?");
    await expect(page.getByTestId("shortcuts-help")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("shortcuts-help")).toBeHidden();
    await expect(page).toHaveURL(/status=up/);
  });
});
