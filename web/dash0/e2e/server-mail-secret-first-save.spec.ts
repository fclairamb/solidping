import { test, expect, API_BASE, getAuthToken } from "./fixtures";

// Spec 2026-10-09-02: on a fresh install there is no stored email.password, so
// the form used to skip the password on Save and show "Saved" anyway.
test.describe("Server mail: secret saved on first setup", () => {
  test("saves the SMTP password the first time", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const url = `${API_BASE}/api/v1/system/parameters/email.password`;
    const headers = { Authorization: `Bearer ${token}` };

    await page.request.delete(url, { headers });

    try {
      await page.goto("/d/orgs/test/server/mail");
      await expect(page.locator("#password")).toBeEnabled();

      await page.locator("#host").fill("  smtp.acme.com  ");
      await page.locator("#password").fill("smtp-key-123");
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await expect(page.getByText("Saved")).toBeVisible();

      await page.reload();
      await expect(page.locator("#password")).toBeDisabled();
      await expect(page.locator("#password")).toHaveValue("******");
      await expect(page.getByRole("button", { name: "Edit" })).toBeVisible();
      await expect(page.locator("#host")).toHaveValue("smtp.acme.com");

      const resp = await page.request.get(url, { headers });
      expect(resp.ok()).toBeTruthy();
      const body = await resp.json();
      expect(body.secret).toBe(true);
    } finally {
      await page.request.delete(url, { headers });
    }
  });
});
