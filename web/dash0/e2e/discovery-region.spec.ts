import { test, expect, type Page } from "@playwright/test";
import { DASH_BASE } from "./fixtures";

/**
 * Running a LAN scan from a private location (spec 2026-10-22-05). The E2E
 * server has no private region, so /regions and /discovery/types are routed.
 */
const REGIONS = {
  data: [
    { slug: "eu-west-1", emoji: "🇮🇪", name: "Ireland" },
    {
      slug: "@dc1",
      emoji: "🏢",
      name: "DC One",
      private: true,
      capabilities: { discovery: "yes" },
    },
    {
      slug: "@dc2",
      emoji: "🏬",
      name: "DC Two",
      private: true,
      capabilities: { discovery: "no" },
    },
  ],
  defaultRegions: [],
};

async function mock(page: Page, serverAvailable: boolean) {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(REGIONS),
    }),
  );
  await page.route("**/api/v1/orgs/*/discovery/types", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          { type: "lan", source: "lan", serverAvailable },
          { type: "container", source: "container" },
        ],
      }),
    }),
  );
}

test.describe("Discovery from a private location", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(`${DASH_BASE}/orgs/test/login`);
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("the picker is lan-only and lists private regions only", async ({ page }) => {
    await mock(page, true);
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new?method=lan`);

    const select = page.getByTestId("discovery-region-select");
    await expect(select).toBeVisible();
    await select.click();
    await expect(page.getByTestId("discovery-region-option-server")).toBeVisible();
    await expect(page.getByTestId("discovery-region-option-@dc1")).toBeVisible();
    await expect(page.getByTestId("discovery-region-option-@dc2")).toHaveAttribute(
      "data-disabled",
      "",
    );
    await expect(page.getByTestId("discovery-region-option-eu-west-1")).toHaveCount(0);
    await page.keyboard.press("Escape");

    await page.getByRole("combobox", { name: /scan method/i }).click();
    await page.getByRole("option", { name: /containers/i }).click();
    await expect(page.getByTestId("discovery-region-select")).toHaveCount(0);
  });

  test("when the server cannot scan, Server is absent and a region is required", async ({
    page,
  }) => {
    await mock(page, false);
    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new?method=lan`);

    await page.fill("textarea", "10.0.0.0/24");
    await page.getByRole("checkbox").check();
    const submit = page.getByRole("button", { name: /start scan/i });
    await expect(submit).toBeDisabled();

    await page.getByTestId("discovery-region-select").click();
    await expect(page.getByTestId("discovery-region-option-server")).toHaveCount(0);
    await page.getByTestId("discovery-region-option-@dc1").click();
    await expect(submit).toBeEnabled();
  });

  test("the request carries the region and a refusal is translated", async ({ page }) => {
    await mock(page, true);
    let body: Record<string, unknown> | undefined;
    await page.route("**/api/v1/orgs/*/discovery/scans", async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      body = route.request().postDataJSON();
      await route.fulfill({
        status: 422,
        contentType: "application/json",
        body: JSON.stringify({
          title: "no agent",
          code: "DISCOVERY_REGION_UNAVAILABLE",
        }),
      });
    });

    await page.goto(`${DASH_BASE}/orgs/test/organization/discovery/new?method=lan`);
    await page.getByTestId("discovery-region-select").click();
    await page.getByTestId("discovery-region-option-@dc1").click();
    await page.fill("textarea", "10.0.0.0/24");
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: /start scan/i }).click();

    await expect(
      page.getByText(/No agent of this location is ready to scan/),
    ).toBeVisible();
    expect(body?.region).toBe("@dc1");
    expect(body?.type).toBe("lan");
    // Positive control: still on the form.
    await expect(page).toHaveURL(/\/discovery\/new/);
  });
});
