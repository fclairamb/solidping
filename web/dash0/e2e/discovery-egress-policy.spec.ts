import { test, expect } from "@playwright/test";
import { DASH_BASE } from "./fixtures";

/**
 * Discovery under an egress policy (spec 2026-10-16-10). A deployment that
 * refuses private targets hides `lan` (and, on SaaS, `container`) from
 * GET /discovery/types and answers 403 DISCOVERY_NOT_AVAILABLE when one is
 * started anyway. The E2E server is self-hosted (everything offered), so the
 * refused deployment is simulated by routing those two API answers.
 */
test.describe("Discovery under an egress policy", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/login`);
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("the new-scan form falls back to an offered method when lan is hidden", async ({
    page,
  }) => {
    await page.route("**/api/v1/orgs/*/discovery/types", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [{ type: "container", source: "container" }] }),
      });
    });

    // The list page's "New scan" button links to ?method=lan.
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new?method=lan`);

    await expect(page.getByTestId("discovery-container-hosts-input")).toBeVisible();
    await expect(page.getByTestId("discovery-cidrs-input")).toHaveCount(0);
    await expect(page.getByRole("combobox", { name: /scan method/i })).toHaveText(
      /containers/i,
    );
  });

  test("a refused scan shows the not-available message", async ({ page }) => {
    let refused = 0;
    await page.route("**/api/v1/orgs/*/discovery/scans", async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      refused++;
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({
          title: "LAN discovery is not available",
          code: "DISCOVERY_NOT_AVAILABLE",
        }),
      });
    });

    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new?method=lan`);
    await page.fill("textarea", "10.0.0.0/24");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();

    await expect(
      page.locator("[data-sonner-toaster]"),
    ).toContainText("This discovery source is not available on this server.");
    expect(refused).toBe(1);
    // Positive control: still on the form, nothing navigated to a scan page.
    await expect(page).toHaveURL(/\/discovery\/new/);
  });
});
