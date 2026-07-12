import { test, expect, API_BASE, type Page } from "./fixtures";

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
): Promise<{ uid: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name,
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

async function deleteSchedule(page: Page, token: string, uid: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/on-call-schedules/${uid}`,
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
    const originalName = `E2E Edit ${stamp}`;
    const { uid } = await createSchedule(page, token, originalName);

    try {
      // Visit the detail page (addressed by uid); the Edit button is a route link.
      await page.goto(`orgs/test/on-call/${uid}`);
      await page.waitForLoadState("networkidle");
      await expect(
        page.getByRole("heading", { name: originalName }),
      ).toBeVisible();

      // Breadcrumb (scoped to the header) reads `params.uid`: the section root
      // is a clickable back-link and the entity-name segment renders the
      // schedule name — both broke when it still read the removed `slug`.
      const header = page.locator("header").first();
      await expect(
        header.getByRole("link", { name: /on-call/i }),
      ).toBeVisible();
      await expect(header.getByText(originalName)).toBeVisible();

      await page.getByTestId("oncall-edit-button").click();
      await page.waitForURL(
        (url) => url.pathname.endsWith(`/on-call/${uid}/edit`),
      );
      await page.waitForLoadState("networkidle");

      // The form pre-fills with the current name.
      const nameInput = page.getByLabel(/^name/i).first();
      await expect(nameInput).toHaveValue(originalName);

      const updatedName = `${originalName} edited`;
      await nameInput.fill(updatedName);

      // Submit; expect navigation back to /on-call/$uid (without /edit).
      await page.getByRole("button", { name: /save|update/i }).first().click();
      await page.waitForURL(
        (url) =>
          url.pathname.endsWith(`/on-call/${uid}`) &&
          !url.pathname.endsWith("/edit"),
      );
      await page.waitForLoadState("networkidle");

      await expect(
        page.getByRole("heading", { name: updatedName }),
      ).toBeVisible();
    } finally {
      await deleteSchedule(page, token, uid);
    }
  });
});
