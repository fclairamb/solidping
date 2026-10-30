import { test, expect, type Page } from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

// Spec 2026-10-28-06: regions advertise an `icmp` capability. The region
// picker marks a region that reports "no" for an ICMP check, but never hides
// or disables it, and never marks "unknown".

const REGIONS = [
  { slug: "no-ping", emoji: "🇯🇵", name: "No Ping Region", capabilities: { icmp: "no" } },
  { slug: "silent", emoji: "🇩🇪", name: "Silent Region" },
  { slug: "can-ping", emoji: "🇫🇷", name: "Can Ping Region", capabilities: { icmp: "yes" } },
];

async function mockRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS, defaultRegions: ["can-ping"] }),
    }),
  );
}

test.describe("region picker icmp capability", () => {
  test("flags a region that cannot ping, keeps it selectable, ignores unknown", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=icmp");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    await expect(page.getByTestId("region-icmp-no-no-ping")).toBeVisible();
    await expect(page.getByTestId("region-icmp-no-silent")).toHaveCount(0);
    await expect(page.getByTestId("region-icmp-no-can-ping")).toHaveCount(0);
    await expect(page.getByTestId("region-option-silent")).toHaveAttribute("data-icmp", "unknown");

    await expect(page.getByTestId("region-option-no-ping").getByRole("checkbox")).toBeEnabled();
  });

  test("stays quiet for a non-icmp check", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await choosePinnedRegions(page);

    await expect(page.getByTestId("region-icmp-no-no-ping")).toHaveCount(0);
  });
});
