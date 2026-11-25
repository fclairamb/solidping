import { test, expect, API_BASE, getAuthToken } from "./fixtures";

// Spec 2026-11-25-03: provider cards on Server Settings > Authentication are
// collapsed on load unless the provider is enabled or configured.
const KEYS = ["auth.gitlab.client_id", "auth.gitlab.client_secret", "auth.gitlab.enabled"];

test.describe("Server auth: collapsible provider cards", () => {
  test("unconfigured cards are collapsed, configured ones are open", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };
    const cleanup = async () => {
      for (const key of KEYS) {
        await page.request.delete(`${API_BASE}/api/v1/system/parameters/${key}`, { headers });
      }
    };
    await cleanup();

    try {
      await page.goto("/d/orgs/test/server/auth");
      const field = page.getByTestId("provider-field-auth.gitlab.client_id");
      await expect(page.getByTestId("provider-card-gitlab")).toBeVisible();
      await expect(page.getByTestId("provider-card-status-gitlab")).toHaveAttribute(
        "data-status",
        "notConfigured",
      );
      await expect(field).toHaveCount(0);

      // Expanding shows the fields.
      await page.getByTestId("provider-card-toggle-gitlab").click();
      await expect(field).toBeVisible();

      // Saving a client id makes the provider "configured"; it stays open on reload.
      await field.fill("acme-gitlab-id");
      await page.getByTestId("provider-field-auth.gitlab.client_secret").fill("acme-secret");
      await page.getByTestId("provider-save-gitlab").click();
      await expect(page.getByTestId("settings-saved")).toBeVisible();

      await page.reload();
      await expect(page.getByTestId("provider-card-status-gitlab")).toHaveAttribute(
        "data-status",
        "configured",
      );
      await expect(page.getByTestId("provider-field-auth.gitlab.client_id")).toBeVisible();

      // Other, untouched providers stay collapsed.
      await expect(page.getByTestId("provider-field-auth.github.client_id")).toHaveCount(0);
    } finally {
      await cleanup();
    }
  });
});
