import { API_BASE, expect, getAuthToken, test, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Incident reminders, spec 2026-10-12-16: the per-check interval in the check
// form's incident-tracking section, and the org default on the settings page.
// Which incidents get reminded, and when, is the backend's job and is covered
// by the Go tests (jobtypes/job_incident_reminder_test.go,
// handlers/incidents/reminder_test.go).

async function createCheck(page: Page, token: string, name: string): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, type: "http", config: { url: "https://example.com/reminders" } },
  });
  expect(resp.ok()).toBeTruthy();

  return ((await resp.json()) as { uid: string }).uid;
}

async function deleteCheck(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function setOrgReminderDefault(page: Page, token: string, seconds: number): Promise<void> {
  const resp = await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { reminderIntervalSeconds: seconds },
  });
  expect(resp.ok()).toBeTruthy();
}

test.describe("Incident reminders", () => {
  test("a check's reminder interval is saved and survives a reload", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const uid = await createCheck(page, token, `E2E Reminders ${Date.now()}`);

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await expandSection(page, "section-incident-tracking-trigger");

      const select = page.getByTestId("reminder-interval-select");
      await expect(select).toBeVisible();
      // A new check inherits the org default.
      await expect(select).toContainText("Org default");

      await select.click();
      await page.getByTestId("reminder-interval-option-1800").click();
      await expect(select).toContainText("30 min");

      const [request] = await Promise.all([
        page.waitForRequest(
          (req) => req.url().includes(`/api/v1/orgs/test/checks/${uid}`) && req.method() === "PATCH",
        ),
        page.getByTestId("check-submit-button").click(),
      ]);
      expect((request.postDataJSON() as Record<string, unknown>).reminderIntervalSeconds).toBe(1800);

      const stored = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      expect(((await stored.json()) as Record<string, unknown>).reminderIntervalSeconds).toBe(1800);

      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.reload();
      await expandSection(page, "section-incident-tracking-trigger");
      await expect(page.getByTestId("reminder-interval-select")).toContainText("30 min");
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("the org default is set in settings and shown on a new check", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    try {
      await page.goto("orgs/test/organization/settings");

      const select = page.getByTestId("reminder-interval-default-select");
      await expect(select).toBeVisible();
      await select.click();
      await page.getByTestId("reminder-interval-default-option-3600").click();

      const [response] = await Promise.all([
        page.waitForResponse(
          (resp) => resp.url().includes("/api/v1/orgs/test/settings") && resp.request().method() === "PATCH",
        ),
        page.getByTestId("reminder-interval-default-save").click(),
      ]);
      expect(response.status()).toBe(200);
      expect(((await response.json()) as Record<string, unknown>).reminderIntervalSeconds).toBe(3600);

      await page.reload();
      await expect(page.getByTestId("reminder-interval-default-select")).toContainText("1 h");

      await page.goto("orgs/test/checks/new?checkType=http");
      await expect(page.getByTestId("check-name-input")).toBeVisible();
      await expandSection(page, "section-incident-tracking-trigger");
      await expect(page.getByTestId("reminder-interval-select")).toContainText("Org default (1 h)");
    } finally {
      // The "test" org is shared by every spec: put it back to off.
      await setOrgReminderDefault(page, token, 0);
    }
  });
});
