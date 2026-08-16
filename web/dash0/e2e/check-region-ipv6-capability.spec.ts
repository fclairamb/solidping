import { test, expect, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// Coverage for spec 2026-08-15-11: regions advertise a three-state IPv6 egress
// capability, and the region picker uses it to mark and sort — never to hide,
// disable or reject.
//
// The region list is mocked so all three states are present deterministically:
// a real deployment's regions all report the same thing, and "unknown" only
// appears when a worker is stale or old — not something an E2E run can arrange.
// What the mock does NOT fake is the behaviour under test: the marking, the
// sort-on-pin, and the fact that every region stays selectable.

const REGIONS = [
  { slug: "no-v6", emoji: "🇩🇪", name: "No V6 Region", capabilities: { ipv6: "no" } },
  { slug: "silent", emoji: "🇯🇵", name: "Silent Region" }, // no capabilities map at all
  { slug: "has-v6", emoji: "🇫🇷", name: "Has V6 Region", capabilities: { ipv6: "yes" } },
];

async function mockRegions(page: Page): Promise<void> {
  await page.route("**/api/v1/orgs/*/regions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: REGIONS, defaultRegions: ["no-v6"] }),
    }),
  );
}

async function regionOrder(page: Page): Promise<string[]> {
  return await page
    .locator("[data-testid^='region-option-']")
    .evaluateAll((els) =>
      els.map((el) => el.getAttribute("data-testid")!.replace("region-option-", "")),
    );
}

test.describe("region picker IPv6 capability", () => {
  test("marks capable regions and sorts them first when ipv6 is pinned", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockRegions(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Every region is offered, in the server's order, before anything is pinned.
    expect(await regionOrder(page)).toEqual(["no-v6", "silent", "has-v6"]);

    // A capable region is marked; a region that reports "no" is marked as such;
    // a region that reports nothing is NOT marked "no" — that conflation is the
    // exact lie this spec exists to stop telling.
    await expect(page.getByTestId("region-ipv6-has-v6")).toHaveAttribute("data-ipv6", "yes");
    await expect(page.getByTestId("region-ipv6-no-v6")).toHaveAttribute("data-ipv6", "no");
    await expect(page.getByTestId("region-option-silent")).toHaveAttribute(
      "data-ipv6",
      "unknown",
    );

    // Pin the check to IPv6.
    await expandSection(page, "section-advanced-trigger");
    await page.getByTestId("check-ip-version-select").click();
    await page.getByRole("option", { name: "IPv6 only", exact: true }).click();

    // Capable first, then unknown, then the region that says it cannot.
    await expect
      .poll(() => regionOrder(page))
      .toEqual(["has-v6", "silent", "no-v6"]);

    // Unknown now carries its own neutral badge, still distinct from "no".
    await expect(page.getByTestId("region-ipv6-silent")).toHaveAttribute(
      "data-ipv6",
      "unknown",
    );
    await expect(page.getByTestId("regions-ipv6-hint")).toBeVisible();

    // Nothing is hidden and nothing is disabled — the advertised value is a
    // hint, never a gate. A region advertising "no" must still be selectable,
    // because the run-time egress probe is the authority and the region may
    // have gained IPv6 since its last heartbeat.
    expect(await page.locator("[data-testid^='region-option-']").count()).toBe(3);

    const noV6Checkbox = page.getByTestId("region-option-no-v6").getByRole("checkbox");
    await expect(noV6Checkbox).toBeEnabled();
    await noV6Checkbox.click();
    await expect(noV6Checkbox).toHaveAttribute("data-state", "checked");
  });
});
