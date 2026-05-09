import { test, expect, type Page } from "./fixtures";

const API_BASE = "http://localhost:4000";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createSchedule(
  page: Page,
  token: string,
  name: string,
  slug: string,
) {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name,
        slug,
        timezone: "UTC",
        rotationType: "daily",
        handoffTime: "09:00",
        startAt: new Date().toISOString(),
        userUids: [],
      },
    },
  );
  return resp.json();
}

async function deleteSchedule(page: Page, token: string, slug: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules/${slug}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
}

test.describe("On-call schedule edit page", () => {
  test("edit page loads, saves a name change, returns to detail", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const stamp = Date.now();
    const slug = `e2e-edit-${stamp}`;
    const originalName = `E2E Edit ${stamp}`;
    await createSchedule(page, token, originalName, slug);

    try {
      // Visit the detail page; the Edit button is now a route link.
      await page.goto(`orgs/test/on-call/${slug}`);
      await page.waitForLoadState("networkidle");
      await expect(
        page.getByRole("heading", { name: originalName }),
      ).toBeVisible();

      await page.getByTestId("oncall-edit-button").click();
      await page.waitForURL(
        (url) => url.pathname.endsWith(`/on-call/${slug}/edit`),
      );
      await page.waitForLoadState("networkidle");

      // The form pre-fills with the current name.
      const nameInput = page.getByLabel(/^name/i).first();
      await expect(nameInput).toHaveValue(originalName);

      const updatedName = `${originalName} edited`;
      await nameInput.fill(updatedName);

      // Submit; expect navigation back to /on-call/$slug (without /edit).
      await page.getByRole("button", { name: /save|update/i }).first().click();
      await page.waitForURL(
        (url) =>
          url.pathname.endsWith(`/on-call/${slug}`) &&
          !url.pathname.endsWith("/edit"),
      );
      await page.waitForLoadState("networkidle");

      await expect(
        page.getByRole("heading", { name: updatedName }),
      ).toBeVisible();
    } finally {
      await deleteSchedule(page, token, slug);
    }
  });
});
