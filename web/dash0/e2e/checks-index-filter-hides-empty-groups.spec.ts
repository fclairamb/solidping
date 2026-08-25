import { test, expect, type Page } from "./fixtures";

// Coverage for spec 2026-08-02-07 (GitHub issue #171): filtering the checks
// list (status/labels/search/internal) must hide group sections whose bucket
// of *matching* checks is empty, instead of rendering every group
// unconditionally with a "no checks" placeholder. Clearing the filter must
// bring every group back.
//
// Like check-group-collapse.spec.ts, this mocks `/check-groups` and the
// paginated `/checks` endpoint directly so the scenario (one all-up group,
// one group with a down check) is deterministic and fast — real checks would
// need to actually run and fail, which is slow and non-deterministic.

interface MockCheck {
  uid: string;
  name: string;
  status: string;
  checkGroupUid: string;
}

interface MockGroup {
  uid: string;
  name: string;
  status: string;
  memberStatusCounts: Record<string, number>;
  checkCount: number;
}

async function mockChecksIndex(
  page: Page,
  opts: { groups: MockGroup[]; checks: MockCheck[] },
): Promise<void> {
  await page.route("**/api/v1/orgs/*/check-groups*", (route) => {
    const url = route.request().url();
    if (!url.includes("/check-groups")) return route.continue();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.groups.map((g, idx) => ({
          uid: g.uid,
          name: g.name,
          slug: g.uid,
          sortOrder: idx,
          checkCount: g.checkCount,
          status: g.status,
          memberStatusCounts: g.memberStatusCounts,
          escalationPolicyUid: null,
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        })),
      }),
    });
  });

  // The paginated checks endpoint is server-filtered — mirror that here by
  // honoring the `status` query param, since that's exactly the mismatch the
  // spec is about: this endpoint is filtered, /check-groups never is.
  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (!url.includes("/checks") || url.includes("/check-groups")) {
      return route.continue();
    }
    const statusParam = new URL(url).searchParams.get("status");
    const filtered = statusParam
      ? opts.checks.filter((c) => c.status === statusParam)
      : opts.checks;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: filtered.map((c) => ({
          uid: c.uid,
          name: c.name,
          slug: c.uid,
          type: "http",
          enabled: true,
          checkGroupUid: c.checkGroupUid,
          status: c.status,
          lastResult: { status: c.status, durationMs: 42 },
          config: { url: `https://example.com/${c.uid}` },
        })),
        // No cursor: a single page, so hasNextPage is false right away and
        // each group's "no checks" vs "hidden" decision resolves immediately.
        pagination: { total: filtered.length },
      }),
    });
  });

  await page.route("**/api/v1/orgs/*/escalation-policies*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );
}

test.describe("Checks list hides empty groups while filtering", () => {
  test("filtering by status hides the all-up group, keeps the group with a down check, and clearing brings both back", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const allUpGroup: MockGroup = {
      uid: "e2e-hideempty-allup",
      name: "E2E HideEmpty AllUp Group",
      status: "up",
      memberStatusCounts: { up: 2 },
      checkCount: 2,
    };
    const hasDownGroup: MockGroup = {
      uid: "e2e-hideempty-hasdown",
      name: "E2E HideEmpty HasDown Group",
      status: "down",
      memberStatusCounts: { down: 1, up: 1 },
      checkCount: 2,
    };

    const checks: MockCheck[] = [
      { uid: "e2e-hideempty-a1", name: "AllUp Check A1", status: "up", checkGroupUid: allUpGroup.uid },
      { uid: "e2e-hideempty-a2", name: "AllUp Check A2", status: "up", checkGroupUid: allUpGroup.uid },
      { uid: "e2e-hideempty-b1", name: "HasDown Check B1", status: "up", checkGroupUid: hasDownGroup.uid },
      { uid: "e2e-hideempty-b2", name: "HasDown Check B2", status: "down", checkGroupUid: hasDownGroup.uid },
    ];

    await mockChecksIndex(page, { groups: [allUpGroup, hasDownGroup], checks });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const allUpSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(allUpGroup.name) });
    const hasDownSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(hasDownGroup.name) });

    // Unfiltered: both groups render, with their unfiltered checkCount badges.
    await expect(allUpSection).toBeVisible({ timeout: 10000 });
    await expect(hasDownSection).toBeVisible({ timeout: 10000 });
    await expect(hasDownSection.getByTestId("group-check-count")).toHaveText("2");

    // Filter to status=down via the toolbar's faceted popover filter.
    await page.getByTestId("status-filter").click();
    await page.getByTestId("status-filter-option-down").click();
    await page.keyboard.press("Escape");
    await page.waitForLoadState("networkidle");

    // The all-up group has zero matching checks — hidden entirely, no
    // "no checks" placeholder left behind.
    await expect(allUpSection).toHaveCount(0);

    // The group containing the down check stays, showing only the matching
    // row and a header badge reflecting the matching count (1), not the
    // unfiltered checkCount (2).
    await expect(hasDownSection).toBeVisible({ timeout: 10000 });
    await expect(hasDownSection.getByText("HasDown Check B2")).toBeVisible();
    await expect(hasDownSection.getByText("HasDown Check B1")).not.toBeVisible();
    await expect(hasDownSection.getByTestId("group-check-count")).toHaveText("1");

    // Clear the filter — unticking the same checkbox brings both groups back.
    await page.getByTestId("status-filter").click();
    await page.getByTestId("status-filter-option-down").click();
    await page.keyboard.press("Escape");
    await page.waitForLoadState("networkidle");

    await expect(allUpSection).toBeVisible({ timeout: 10000 });
    await expect(hasDownSection).toBeVisible({ timeout: 10000 });
    await expect(hasDownSection.getByTestId("group-check-count")).toHaveText("2");
  });
});
