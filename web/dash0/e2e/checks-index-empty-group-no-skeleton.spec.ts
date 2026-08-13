import { test, expect, type Page } from "./fixtures";

// Regression guard for spec 2026-08-12-02: a group with zero checks
// (group.checkCount === 0) must show the empty-state text immediately, never
// skeleton placeholders — even while the page-level infinite-scroll stream
// still has more pages to give (hasNextPage true, e.g. because the org has
// more checks than fit on one page and the user hasn't scrolled down to it).
//
// Before the fix, CheckGroupSection folded the page-level
// hasNextPage/isFetchingNextPage into its per-group "isLoading", so an empty
// bucket "read as loading" for as long as the stream could still deliver more
// — i.e. forever, since no page can ever produce a row for a genuinely empty
// group. The header badge already knew the truth (group.checkCount): this
// test asserts the body agrees with it.
//
// Mirrors checks-index-filter-hides-empty-groups.spec.ts and
// checks-index-group-pagination.spec.ts: mock `/check-groups` and the
// paginated `/checks` endpoint directly so the scenario (one non-empty group
// fully on page 1, one genuinely empty group, and a next-page cursor that
// keeps hasNextPage true) is deterministic and fast.

const NO_CHECKS_TEXT = "No checks in this group";

interface MockGroup {
  uid: string;
  name: string;
  status: string;
  checkCount: number;
}

async function mockChecksIndex(
  page: Page,
  opts: { groups: MockGroup[]; nonEmptyGroupUid: string; checkCount: number },
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
          memberStatusCounts: g.checkCount > 0 ? { up: g.checkCount } : {},
          escalationPolicyUid: null,
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        })),
      }),
    });
  });

  // Page 1 (no `cursor` param) carries a next-page cursor so `hasNextPage`
  // goes true on first load — simulating an org with more checks than fit on
  // one page, which is exactly the scenario that made the empty group's
  // bucket "read as loading" forever before the fix (see checks.index.tsx's
  // `checksStreaming`). The page-level infinite-scroll sentinel can be
  // in-view on a short page and auto-fire `fetchNextPage`, and a poll
  // (refetchInterval) re-fetches every already-loaded page including page 1
  // — so branching on the `cursor` query param (not a call counter) keeps
  // page 1 always answering with the same 3 checks on every re-fetch, while
  // page 2+ (cursor="more-pages-available") returns no further checks and no
  // next cursor so the stream settles instead of looping.
  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (!url.includes("/checks") || url.includes("/check-groups")) {
      return route.continue();
    }
    const isFirstPage = new URL(url).searchParams.get("cursor") === null;
    const checks = isFirstPage
      ? Array.from({ length: opts.checkCount }, (_, i) => ({
          uid: `e2e-emptygroup-${i}`,
          name: `Non-Empty Group Check ${i}`,
          slug: `e2e-emptygroup-${i}`,
          type: "http",
          enabled: true,
          checkGroupUid: opts.nonEmptyGroupUid,
          status: "up",
          lastResult: { status: "up", durationMs: 42 },
          config: { url: `https://example.com/${i}` },
        }))
      : [];
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: checks,
        pagination: {
          total: opts.checkCount,
          cursor: isFirstPage ? "more-pages-available" : undefined,
        },
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

test.describe("Checks index empty group", () => {
  test("a group with checkCount 0 shows the empty state immediately, never skeletons, while hasNextPage stays true", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const nonEmptyGroup: MockGroup = {
      uid: "e2e-emptygroup-nonempty",
      name: "E2E EmptyGroup NonEmpty",
      // Deliberately not "up": groups whose rollup status is "up" start
      // collapsed (see checks.index.tsx's `defaultCollapsed`), which would
      // hide this group's rows and make the "rows render, no skeletons"
      // assertion below moot.
      status: "down",
      checkCount: 3,
    };
    const emptyGroup: MockGroup = {
      uid: "e2e-emptygroup-empty",
      name: "E2E EmptyGroup Empty",
      status: "created",
      checkCount: 0,
    };

    await mockChecksIndex(page, {
      groups: [nonEmptyGroup, emptyGroup],
      nonEmptyGroupUid: nonEmptyGroup.uid,
      checkCount: 3,
    });

    await page.goto("orgs/test/checks");

    const sectionByName = (name: string) =>
      page
        .getByTestId("group-section")
        .filter({ has: page.getByTestId("group-name").getByText(name) });

    const nonEmptySection = sectionByName(nonEmptyGroup.name);
    const emptySection = sectionByName(emptyGroup.name);

    await expect(nonEmptySection).toBeVisible({ timeout: 10000 });
    await expect(emptySection).toBeVisible({ timeout: 10000 });

    // Header badge already shows the true, authoritative count.
    await expect(emptySection.getByTestId("group-check-count")).toHaveText("0");

    // The empty group's body shows the truthful empty state right away — no
    // skeleton rows, even though the page-level stream still has a next page
    // (hasNextPage true, since the mocked response always carries a cursor).
    await expect(emptySection.getByText(NO_CHECKS_TEXT)).toBeVisible({
      timeout: 10000,
    });
    await expect(emptySection.locator(".animate-pulse")).toHaveCount(0);

    // A link into the check-creation flow, pre-filled with this group, is
    // offered from the empty state.
    await expect(
      emptySection.getByTestId("group-empty-new-check-link"),
    ).toBeVisible();

    // The non-empty group, which IS fully loaded (all 3 of its 3 checks are
    // on page 1), renders its real rows — never a false "No checks" and never
    // stuck skeletons either, confirming this fix didn't disturb the existing
    // "rows win over loading" behavior for populated groups.
    await expect(nonEmptySection.getByText(NO_CHECKS_TEXT)).toHaveCount(0);
    await expect(nonEmptySection.locator(".animate-pulse")).toHaveCount(0);
    await expect(
      nonEmptySection.getByText("Non-Empty Group Check 0"),
    ).toBeVisible();

    // Settle a beat and re-assert: the empty state must not have been a
    // one-frame flash that later regresses back into skeletons once
    // checksStreaming's memoized value stabilizes.
    await page.waitForTimeout(500);
    await expect(emptySection.getByText(NO_CHECKS_TEXT)).toBeVisible();
    await expect(emptySection.locator(".animate-pulse")).toHaveCount(0);
  });
});
