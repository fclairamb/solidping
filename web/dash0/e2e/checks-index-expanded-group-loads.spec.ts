import { test, expect, type Page } from "./fixtures";
import { groupSectionLocator, checkNameLocator } from "./locators";

// Regression guard for fclairamb/solidping-business#120: on the checks index
// (groups mode, no filter), an expanded group whose checks sit on a page of
// the sort=group stream that is not fetched yet showed skeletons forever. The
// only trigger for the next page was the page-level sentinel below the last
// section, which many sections below an open group never reach.
//
// The API is mocked so the layout is deterministic: group 0 ("up", collapsed)
// fills page 1 with 100 checks, the next 60 groups hold 2 checks each on page 2.
// Group 1 is "down", so it starts expanded near the top of the page, while the
// 59 collapsed groups after it push the page-level sentinel far out of reach.

const GROUP_COUNT = 61;
const NOW = "2026-11-28T00:00:00Z";

function group(i: number) {
  return {
    uid: `00000000-0000-4000-8000-${String(i).padStart(12, "0")}`,
    name: `Mock group ${i}`,
    slug: `mock-group-${i}`,
    sortOrder: i * 10,
    checkCount: i === 0 ? 100 : 2,
    status: i === 1 ? "down" : "up",
    memberStatusCounts: i === 1 ? { down: 2 } : { up: i === 0 ? 100 : 2 },
    escalationPolicyUid: null,
    createdAt: NOW,
    updatedAt: NOW,
  };
}

function check(groupIndex: number, n: number) {
  const slug = `mock-${groupIndex}-${n}`;
  return {
    uid: `10000000-0000-4000-8000-${String(groupIndex * 1000 + n).padStart(12, "0")}`,
    name: `Mock check ${groupIndex}-${n}`,
    slug,
    type: "http",
    config: { url: `https://example.com/${slug}` },
    enabled: true,
    internal: false,
    period: "00:01:00",
    labels: {},
    checkGroupUid: group(groupIndex).uid,
    createdAt: NOW,
    updatedAt: NOW,
  };
}

async function mockChecksIndex(page: Page) {
  const groups = Array.from({ length: GROUP_COUNT }, (_, i) => group(i));
  const page1 = Array.from({ length: 100 }, (_, n) => check(0, n));
  const page2 = groups.slice(1).flatMap((_, i) => [check(i + 1, 0), check(i + 1, 1)]);

  await page.route("**/api/v1/orgs/test/check-groups", (route) =>
    route.fulfill({ json: { data: groups } }),
  );
  await page.route(
    (url) => url.pathname === "/api/v1/orgs/test/checks",
    (route) => {
      const cursor = new URL(route.request().url()).searchParams.get("cursor");
      return route.fulfill({
        json: cursor
          ? { data: page2, pagination: { limit: 100 } }
          : { data: page1, pagination: { limit: 100, cursor: "page-2" } },
      });
    },
  );
}

test.describe("Checks index: expanded group on a later page", () => {
  test("loads its checks without scrolling to the bottom", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockChecksIndex(page);

    await page.goto("orgs/test/checks");

    const openGroup = groupSectionLocator(page, "Mock group 1");
    await expect(openGroup).toBeVisible({ timeout: 15_000 });
    await expect(openGroup.getByTestId("group-header")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    // Positive control: the page-level sentinel (below the last section) is
    // beyond its 1200px root margin, so only the open group itself can have
    // asked for page 2.
    await expect(groupSectionLocator(page, `Mock group ${GROUP_COUNT - 1}`)).toBeAttached();
    const lastSectionBottom = await page.evaluate(() => {
      const sections = document.querySelectorAll('[data-testid="group-section"]');
      return sections[sections.length - 1].getBoundingClientRect().bottom;
    });
    const viewportHeight = page.viewportSize()?.height ?? 0;
    expect(lastSectionBottom).toBeGreaterThan(viewportHeight + 1200);

    await expect(checkNameLocator(openGroup, "Mock check 1-0")).toBeVisible({
      timeout: 10_000,
    });
    await expect(checkNameLocator(openGroup, "Mock check 1-1")).toBeVisible();
  });
});

