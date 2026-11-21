import { test, expect, API_BASE, stubSavedPreferences, type Page } from "./fixtures";

// Bulk selection bar: a bottom overlay that leaves the rows where they are, a
// checkbox in each table header, and bulk "move to group" / "set period".

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

// English whatever language the shared test account has saved.
const english = (page: Page) => stubSavedPreferences(page, () => ({ locale: "en" }));

async function createGroup(page: Page, token: string, name: string): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/check-groups`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name },
  });
  expect(resp.ok()).toBeTruthy();
  return ((await resp.json()) as { uid: string }).uid;
}

async function createChecks(
  page: Page,
  token: string,
  marker: string,
  count: number,
  checkGroupUid?: string,
): Promise<string[]> {
  const uids: string[] = [];
  for (let i = 0; i < count; i++) {
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `${marker} ${String(i).padStart(2, "0")}`,
        type: "http",
        config: { url: "https://example.com/bulk-group" },
        regions: ["default"],
        checkGroupUid,
      },
    });
    expect(resp.ok()).toBeTruthy();
    uids.push(((await resp.json()) as { uid: string }).uid);
  }
  return uids;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return (await resp.json()) as { checkGroupUid?: string; period: string };
}

async function cleanup(page: Page, token: string, uids: string[], groupUids: string[]) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { checkUids: uids },
  });
  for (const uid of groupUids) {
    await page.request.delete(`${API_BASE}/api/v1/orgs/test/check-groups/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  }
}

test.describe("Bulk selection: overlay, group checkbox, move, period", () => {
  test("the bar floats at the bottom of the screen and does not move the rows", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const marker = `BulkBar${Date.now()}`;
    // Enough rows to overflow the viewport, so the bar has to float.
    const uids = await createChecks(page, token, marker, 15);

    try {
      await english(page);
      await page.goto(`orgs/test/checks?q=${marker}`);
      await expect(page.getByTestId("check-row")).toHaveCount(15);
      const before = await page.getByTestId("check-row").first().boundingBox();

      await page.getByTestId("check-row-checkbox").first().click();
      const bar = page.getByTestId("checks-bulk-bar");
      await expect(bar).toBeVisible();

      const after = await page.getByTestId("check-row").first().boundingBox();
      expect(after?.y).toBe(before?.y);

      // Glued to the bottom of the screen while the last rows are below it.
      await expect(bar).toBeInViewport();
      await expect(page.getByTestId("check-row").last()).not.toBeInViewport();
      const viewport = page.viewportSize();
      const barBox = await bar.boundingBox();
      expect(barBox!.y + barBox!.height).toBeGreaterThan(viewport!.height - 40);
      // Over the page's column, not over the sidebar.
      expect(barBox!.x).toBeGreaterThanOrEqual(before!.x - 1);

      // Automatic placement is an option of the "Set regions" menu.
      await page.getByTestId("checks-bulk-set-regions-button").click();
      await expect(page.getByTestId("checks-bulk-choose-regions-item")).toBeVisible();
      await expect(page.getByTestId("checks-bulk-auto-placement-button")).toBeVisible();
    } finally {
      await cleanup(page, token, uids, []);
    }
  });

  test("the group checkbox selects the group's rows, move to group and set period apply", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const marker = `BulkGrp${Date.now()}`;
    const sourceGroup = await createGroup(page, token, `${marker} source`);
    const targetGroup = await createGroup(page, token, `${marker} target`);
    const grouped = await createChecks(page, token, marker, 2, sourceGroup);
    const loose = await createChecks(page, token, `${marker}x`, 1);

    try {
      await english(page);
      await page.goto(`orgs/test/checks?q=${marker}`);
      await expect(page.getByTestId("check-row")).toHaveCount(3);

      // The header checkbox of the source group's table (groups come first).
      await page.getByTestId("group-select-checkbox").first().click();
      await expect(page.getByTestId("checks-bulk-bar")).toContainText("2 selected");

      await page.getByTestId("checks-bulk-move-group-button").click();
      await page.getByTestId("move-group-select").click();
      await page.getByRole("option", { name: `${marker} target` }).click();
      await page.getByTestId("move-group-confirm").click();
      await expect(page.getByTestId("move-group-dialog")).toBeHidden();

      for (const uid of grouped) {
        await expect.poll(async () => (await getCheck(page, token, uid)).checkGroupUid).toBe(targetGroup);
      }
      expect((await getCheck(page, token, loose[0])).checkGroupUid ?? "").toBe("");

      // The selection survives the move: change the period of the same two.
      await expect(page.getByTestId("checks-bulk-bar")).toContainText("2 selected");
      await page.getByTestId("checks-bulk-set-period-button").click();
      await page.getByTestId("set-period-input").fill("7");
      await expect(page.getByTestId("set-period-preview")).toContainText("2 checks");
      await page.getByTestId("set-period-confirm").click();
      await expect(page.getByTestId("set-period-dialog")).toBeHidden();

      for (const uid of grouped) {
        await expect.poll(async () => (await getCheck(page, token, uid)).period).toBe("00:07:00");
      }
      expect((await getCheck(page, token, loose[0])).period).not.toBe("00:07:00");
    } finally {
      await cleanup(page, token, [...grouped, ...loose], [sourceGroup, targetGroup]);
    }
  });
});
