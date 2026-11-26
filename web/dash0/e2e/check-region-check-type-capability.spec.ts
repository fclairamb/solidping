import { test, expect, type Page } from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

// Spec 2026-11-26-02: a private region advertises one `check-type-<type>`
// verdict per built-in type. The region picker warns when its live agents
// cannot run the check's type, but never disables it (saving stays allowed).

const REGIONS = [
  {
    slug: "@old-agents",
    private: true,
    emoji: "🏢",
    name: "Old Agents",
    capabilities: { "check-type-http": "yes", "check-type-tcp": "no" },
  },
  {
    slug: "@new-agents",
    private: true,
    emoji: "🏢",
    name: "New Agents",
    capabilities: { "check-type-http": "yes", "check-type-tcp": "yes" },
  },
];

async function mockRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS, defaultRegions: ["@new-agents"] }),
    }),
  );
}

test.describe("region picker check type capability", () => {
  test("warns on a region whose agents cannot run the type, keeps it selectable", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    await expect(page.getByTestId("region-check-type-unsupported-@old-agents")).toBeVisible();
    await expect(page.getByTestId("region-check-type-unsupported-@new-agents")).toHaveCount(0);
    await expect(page.getByTestId("region-option-@old-agents").getByRole("checkbox")).toBeEnabled();
  });

  test("stays quiet for a type the region supports", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    await expect(page.getByTestId("region-check-type-unsupported-@old-agents")).toHaveCount(0);
  });
});
