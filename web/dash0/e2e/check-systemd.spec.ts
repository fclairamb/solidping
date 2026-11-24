import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for issue #1000 (systemd check type): the form creates a systemd
// check, rejects a unit name that could be read as a flag, saves the unit and
// the expected state, and the edit form reopens it with them.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("systemd check", () => {
  test("creates a systemd check from the form and saves its unit", async ({
    authenticatedPage,
  }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=systemd");
    await expect(page.getByTestId("check-systemd-unit-input")).toBeVisible();

    // The unit is required.
    await page.getByTestId("check-name-input").fill(`E2E systemd ${Date.now()}`);
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("check-form-error")).toContainText("Unit name is required");

    // A unit that could be read as a systemctl flag is refused.
    await page.getByTestId("check-systemd-unit-input").fill("--host=x");
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("check-form-error")).toContainText("Unit name may only contain");

    await page.getByTestId("check-systemd-unit-input").fill("acme-backup.timer");
    await page.getByTestId("check-systemd-expected-state-select").click();
    await page.getByTestId("check-systemd-expected-state-inactive").click();
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const token = await getAuthToken(page);
    const created = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
    ).json();
    expect(created.type).toBe("systemd");
    expect(created.config.unit).toBe("acme-backup.timer");
    expect(created.config.expected_state).toBe("inactive");

    // The edit form reopens with the saved values.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("check-systemd-unit-input")).toHaveValue("acme-backup.timer");
    await expect(page.getByTestId("check-systemd-expected-state-select")).toContainText(
      "Inactive",
    );
  });
});
