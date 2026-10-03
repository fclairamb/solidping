import { test, expect, API_BASE, getAuthToken, uniqueStamp } from "./fixtures";

// Coverage for spec 2026-10-03-06 (check version history): editing a check
// records a version, the History page shows the diff against the previous
// one, and restoring an older version brings its definition back as a new
// version.

test.describe("Check history", () => {
  test("edit a check, see the diff, restore", async ({ authenticatedPage }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };

    const stamp = uniqueStamp();
    const originalName = `History ${stamp}`;
    const renamed = `History renamed ${stamp}`;

    const createResp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers,
      data: {
        name: originalName,
        type: "http",
        enabled: false,
        config: { url: "https://example.com" },
      },
    });
    expect(createResp.status()).toBe(201);
    const uid = (await createResp.json()).uid as string;

    // Edit through the dashboard.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    const nameInput = page.getByTestId("check-name-input");
    await expect(nameInput).toHaveValue(originalName);
    await nameInput.fill(renamed);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(new RegExp(`/checks/${uid}(\\?|$)`), { timeout: 15000 });

    // History is reached from the check page's menu.
    await page.getByTestId("check-more-actions").click();
    await page.getByTestId("check-history-link").click();
    await page.waitForURL(/\/history/);
    await expect(page.getByTestId("history-breadcrumb")).toBeVisible();

    await expect(page.getByTestId("check-version-row-2")).toBeVisible();
    await expect(page.getByTestId("check-version-row-1")).toBeVisible();

    // The newest version is selected: its diff shows the rename.
    const nameChange = page.getByTestId("check-diff-name");
    await expect(nameChange).toContainText(originalName);
    await expect(nameChange).toContainText(renamed);

    // The live version cannot be restored onto itself.
    await expect(page.getByTestId("check-version-restore")).toHaveCount(0);

    // Restore v1.
    await page.getByTestId("check-version-row-1").click();
    await expect(page).toHaveURL(/version=1/);
    await page.getByTestId("check-version-restore").click();
    await page.getByTestId("check-version-restore-confirm").click();

    await expect(page.getByTestId("check-version-row-3")).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId("check-version-row-3")).toContainText("restored v1");

    const check = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers })
    ).json();
    expect(check.name).toBe(originalName);

    const versions = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}/versions`, { headers })
    ).json();
    expect(versions.data.map((v: { version: number }) => v.version)).toEqual([3, 2, 1]);
    expect(versions.data[0].reason).toBe("restored v1");
    expect(versions.data[0].origin).toBe("user");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers });
  });
});
