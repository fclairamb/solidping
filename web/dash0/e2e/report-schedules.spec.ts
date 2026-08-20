import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-20-01: the scheduled uptime-report CRUD surface
// under Organization -> Uptime reports.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Uptime report schedules", () => {
  test("creates a schedule and lists it without exposing the addresses", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const name = `E2E Report ${Date.now()}`;
    const recipient = `e2e-${Date.now()}@acme.com`;

    await page.goto("orgs/test/organization/report-schedules");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("report-new").click();
    await page.waitForURL(/\/report-schedules\/new/, { timeout: 10000 });

    await page.getByTestId("report-name").fill(name);

    // Recipients are required — a digest with nobody to send to is not a digest.
    await expect(page.getByTestId("report-submit")).toBeDisabled();

    const recipientsInput = page
      .getByTestId("report-recipients")
      .locator("input")
      .first();
    await recipientsInput.fill(recipient);
    await recipientsInput.press("Enter");

    await expect(page.getByTestId("report-submit")).toBeEnabled();
    await page.getByTestId("report-submit").click();

    await page.waitForURL(/\/report-schedules\/[0-9a-f-]{36}/, {
      timeout: 10000,
    });
    const uid = page.url().match(/\/report-schedules\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/report-schedules/${uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(resp.status()).toBe(200);
    const created = await resp.json();
    expect(created.name).toBe(name);
    expect(created.recipients).toEqual([recipient]);
    expect(created.frequency).toBe("monthly");

    // The list page shows a COUNT, never the addresses: recipients are PII and
    // a list view is the easiest place for them to be shoulder-surfed.
    await page.goto("orgs/test/organization/report-schedules");
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("report-row").filter({ hasText: name }).first();
    await expect(row).toBeVisible();
    await expect(row).toContainText("1 recipient");
    await expect(row).not.toContainText(recipient);

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/report-schedules/${uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  });
});
