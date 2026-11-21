import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-11-21-03: select several checks in the list and delete them at once.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createChecks(page: Page, token: string, marker: string, count: number): Promise<string[]> {
  const uids: string[] = [];
  for (let i = 0; i < count; i++) {
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `${marker} ${String(i).padStart(2, "0")}`,
        type: "http",
        config: { url: "https://example.com/bulk-delete" },
        regions: ["default"],
      },
    });
    expect(resp.ok()).toBeTruthy();
    uids.push(((await resp.json()) as { uid: string }).uid);
  }
  return uids;
}

async function exists(page: Page, token: string, uid: string): Promise<boolean> {
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return resp.ok();
}

async function cleanup(page: Page, token: string, uids: string[]) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { checkUids: uids },
  });
}

test.describe("Bulk delete checks", () => {
  test("selects three checks, deletes them, the others remain", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const marker = `BulkDel${Date.now()}`;
    const uids = await createChecks(page, token, marker, 4);

    try {
      await page.goto(`orgs/test/checks?q=${marker}`);
      await expect(page.getByTestId("check-row")).toHaveCount(4);

      const boxes = page.getByTestId("check-row-checkbox");
      for (let i = 0; i < 3; i++) {
        await boxes.nth(i).click();
      }
      await expect(page.getByTestId("checks-bulk-bar")).toContainText("3 selected");

      await page.getByTestId("checks-bulk-delete-button").click();
      await expect(page.getByTestId("checks-bulk-delete-dialog")).toBeVisible();
      await expect(page.getByTestId("checks-bulk-delete-summary")).toContainText("3 checks will be deleted");
      await page.getByTestId("checks-bulk-delete-confirm").click();
      await expect(page.getByTestId("checks-bulk-delete-dialog")).toBeHidden();

      await expect(page.getByTestId("check-row")).toHaveCount(1);
      await expect(page.getByTestId("checks-bulk-bar")).toBeHidden();
      const remaining = await Promise.all(uids.map((uid) => exists(page, token, uid)));
      expect(remaining.filter(Boolean)).toHaveLength(1);
    } finally {
      await cleanup(page, token, uids);
    }
  });

  test("select-all then deselect one, clear empties the selection", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const marker = `BulkAll${Date.now()}`;
    const uids = await createChecks(page, token, marker, 3);

    try {
      await page.goto(`orgs/test/checks?q=${marker}`);
      await expect(page.getByTestId("check-row")).toHaveCount(3);

      await page.getByTestId("checks-select-all").click();
      await expect(page.getByTestId("checks-bulk-bar")).toContainText("3 selected");

      await page.getByTestId("check-row-checkbox").first().click();
      await expect(page.getByTestId("checks-bulk-bar")).toContainText("2 selected");

      await page.getByTestId("checks-bulk-clear-button").click();
      await expect(page.getByTestId("checks-bulk-bar")).toBeHidden();
    } finally {
      await cleanup(page, token, uids);
    }
  });

  test("more than 10 checks need the count typed", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const marker = `BulkMany${Date.now()}`;
    const uids = await createChecks(page, token, marker, 11);

    try {
      await page.goto(`orgs/test/checks?q=${marker}`);
      await expect(page.getByTestId("check-row")).toHaveCount(11);

      await page.getByTestId("checks-select-all").click();
      await page.getByTestId("checks-bulk-delete-button").click();

      const confirm = page.getByTestId("checks-bulk-delete-confirm");
      await expect(page.getByTestId("checks-bulk-delete-summary")).toContainText("11 checks will be deleted");
      await expect(confirm).toBeDisabled();

      await page.getByTestId("checks-bulk-delete-confirm-input").fill("11");
      await expect(confirm).toBeEnabled();
      await confirm.click();

      await expect(page.getByTestId("checks-bulk-delete-dialog")).toBeHidden();
      await expect(page.getByTestId("check-row")).toHaveCount(0);
    } finally {
      await cleanup(page, token, uids);
    }
  });
});
