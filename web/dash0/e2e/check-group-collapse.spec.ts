import { test, expect, type Page } from "./fixtures";

// Coverage for spec 2026-08-01-02: check-group sections gain a status badge +
// compact member summary in their header, and become collapsible — a group
// whose rollup status is "up" defaults to collapsed, anything else defaults
// to expanded, and a manual toggle overrides the default and persists per
// org in localStorage.
//
// The group's header data (status/memberStatusCounts/checkCount) is driven
// entirely by the check-groups list response — independent of which checks
// page has loaded — so these tests mock both `/check-groups` and the
// paginated `/checks` endpoint directly rather than waiting on real check
// runs (which would be slow and non-deterministic). The prerequisite spec
// (2026-08-01-01) already covers the backend's status/memberStatusCounts
// computation with its own tests.

interface MockCheck {
  uid: string;
  name: string;
  status: string;
  checkGroupUid?: string;
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
  opts: { group: MockGroup; checks: MockCheck[] },
): Promise<void> {
  await page.route("**/api/v1/orgs/*/check-groups*", (route) => {
    const url = route.request().url();
    if (!url.includes("/check-groups")) return route.continue();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          {
            uid: opts.group.uid,
            name: opts.group.name,
            slug: opts.group.uid,
            sortOrder: 0,
            checkCount: opts.group.checkCount,
            status: opts.group.status,
            memberStatusCounts: opts.group.memberStatusCounts,
            escalationPolicyUid: null,
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          },
        ],
      }),
    });
  });

  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (!url.includes("/checks") || url.includes("/check-groups")) {
      return route.continue();
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.checks.map((c) => ({
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
        pagination: { total: opts.checks.length },
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

function fourMemberGroup(uid: string, name: string): {
  group: MockGroup;
  degradedChecks: MockCheck[];
  upChecks: MockCheck[];
} {
  const degradedChecks: MockCheck[] = [
    { uid: `${uid}-c1`, name: "TCP", status: "down", checkGroupUid: uid },
    { uid: `${uid}-c2`, name: "HTTP", status: "up", checkGroupUid: uid },
    { uid: `${uid}-c3`, name: "TLS", status: "up", checkGroupUid: uid },
    { uid: `${uid}-c4`, name: "RDP", status: "up", checkGroupUid: uid },
  ];
  const upChecks: MockCheck[] = degradedChecks.map((c) => ({
    ...c,
    status: "up",
  }));
  return {
    group: {
      uid,
      name,
      status: "degraded",
      memberStatusCounts: { down: 1, up: 3 },
      checkCount: 4,
    },
    degradedChecks,
    upChecks,
  };
}

test.describe("Check group collapse", () => {
  test("a group with a down member shows a degraded header and starts expanded", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const { group, degradedChecks } = fourMemberGroup(
      "e2e-degraded-group",
      "E2E Degraded Group",
    );

    await mockChecksIndex(page, { group, checks: degradedChecks });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const section = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(group.name) });
    await expect(section).toBeVisible({ timeout: 10000 });

    // Status badge reads "Degraded" (the group rollup status), and the
    // compact summary shows the down/up split.
    await expect(section.getByTestId("group-status-badge")).toContainText(
      "Degraded",
    );
    await expect(section.getByTestId("group-member-summary")).toHaveText(
      "1 down · 3 up",
    );

    // Non-up status starts expanded — every member row is visible.
    await expect(section.getByText("TCP")).toBeVisible();
    await expect(section.getByText("HTTP")).toBeVisible();
    await expect(section.getByTestId("group-header")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  test("an all-up group collapses by default, hiding rows while the N/N summary remains", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const { upChecks } = fourMemberGroup("e2e-allup-group", "E2E All Up Group");
    const group: MockGroup = {
      uid: "e2e-allup-group",
      name: "E2E All Up Group",
      status: "up",
      memberStatusCounts: { up: 4 },
      checkCount: 4,
    };

    await mockChecksIndex(page, { group, checks: upChecks });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const section = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(group.name) });
    await expect(section).toBeVisible({ timeout: 10000 });

    // Collapsed by default: the summary is visible, the rows are not.
    await expect(section.getByTestId("group-member-summary")).toHaveText(
      "4/4 up",
    );
    await expect(section.getByTestId("group-header")).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    await expect(section.getByText("TCP")).not.toBeVisible();

    // Expanding via the chevron reveals the rows.
    await section.getByTestId("group-collapse-toggle").click();
    await expect(section.getByText("TCP")).toBeVisible();
    await expect(section.getByTestId("group-header")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  test("a manual toggle survives a reload", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const { upChecks } = fourMemberGroup(
      "e2e-persist-group",
      "E2E Persist Group",
    );
    const group: MockGroup = {
      uid: "e2e-persist-group",
      name: "E2E Persist Group",
      status: "up",
      memberStatusCounts: { up: 4 },
      checkCount: 4,
    };

    await mockChecksIndex(page, { group, checks: upChecks });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const section = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(group.name) });
    await expect(section).toBeVisible({ timeout: 10000 });

    // Starts collapsed (status is up); expand it manually.
    await expect(section.getByText("TCP")).not.toBeVisible();
    await section.getByTestId("group-collapse-toggle").click();
    await expect(section.getByText("TCP")).toBeVisible();

    // Re-mock (routes don't survive a reload) and reload — the manual choice
    // must still be in effect, sourced from localStorage.
    await mockChecksIndex(page, { group, checks: upChecks });
    await page.reload();
    await page.waitForLoadState("networkidle");

    const sectionAfterReload = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(group.name) });
    await expect(sectionAfterReload).toBeVisible({ timeout: 10000 });
    await expect(sectionAfterReload.getByText("TCP")).toBeVisible();
    await expect(sectionAfterReload.getByTestId("group-header")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  test("mobile viewport renders the header without horizontal overflow", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const { group, degradedChecks } = fourMemberGroup(
      "e2e-mobile-group",
      "E2E Mobile Group With A Fairly Long Name",
    );

    await page.setViewportSize({ width: 375, height: 812 });
    await mockChecksIndex(page, { group, checks: degradedChecks });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const section = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(group.name) });
    await expect(section).toBeVisible({ timeout: 10000 });
    await expect(section.getByTestId("group-status-badge")).toBeVisible();
    await expect(section.getByTestId("group-member-summary")).toBeVisible();

    // The page itself must not need horizontal scrolling — every wrapped
    // header element stays within the viewport width.
    const hasHorizontalOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);
  });
});
