import { test, expect } from "./fixtures";

// Spec 2026-11-26-03: Organization → Private locations warns when an agent is
// out of date. The agents list is stubbed: a real agent cannot report an old
// version from a browser test.

const AGENTS_URL = "**/api/v1/orgs/test/agents";

function agent(overrides: Record<string, unknown>) {
  return {
    uid: "11111111-1111-1111-1111-111111111111",
    name: "paris-dc1",
    region: "paris",
    fingerprint: "fp",
    status: "active",
    enrolledAt: new Date().toISOString(),
    ...overrides,
  };
}

test.describe("Private locations outdated agents banner", () => {
  test("is shown when an agent runs another version than the server", async ({ authenticatedPage: page }) => {
    await page.route(AGENTS_URL, (route) =>
      route.fulfill({ json: { data: [agent({ version: "0.1.0" })] } }),
    );

    await page.goto("orgs/test/organization/private-locations");

    const banner = page.getByTestId("outdated-agents-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("paris-dc1");
    await expect(banner).toContainText("docker compose pull");
  });

  test("is absent without an outdated agent", async ({ authenticatedPage: page }) => {
    await page.route(AGENTS_URL, (route) =>
      route.fulfill({
        json: {
          data: [
            agent({ version: "0.1.0", versionPinned: true }),
            agent({ uid: "22222222-2222-2222-2222-222222222222", name: "unknown-one", version: null }),
          ],
        },
      }),
    );

    await page.goto("orgs/test/organization/private-locations");

    await expect(page.getByTestId("agents-card")).toBeVisible();
    await expect(page.getByTestId("outdated-agents-banner")).toHaveCount(0);
  });
});
