import { test, expect, API_BASE, DASH_BASE, uniqueStamp } from "./fixtures";
import type { Page } from "@playwright/test";

test.describe("Sidebar User and Org Info", () => {
  test("should display user email in sidebar footer", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    const userMenuButton = page.getByTestId("user-menu-button");
    await expect(userMenuButton).toBeVisible();

    // The test user (test@test.com) has no name set, so email should be the primary display
    await expect(userMenuButton).toContainText("test@test.com");
  });

  test("should display organization name in sidebar header", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();

    // The sidebar header should show the org name "Test Organization"
    await expect(sidebar).toContainText("Test Organization");
  });

  test("should show org switcher with other orgs in user dropdown", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Open user dropdown
    await page.getByTestId("user-menu-button").click();

    // Should show "Switch Organization" label
    await expect(page.getByText("Switch Organization")).toBeVisible();

    // Should show the other two orgs (not the current one)
    await expect(page.getByTestId("switch-org-test2")).toBeVisible();
    await expect(page.getByTestId("switch-org-test3")).toBeVisible();

    // Should show org names
    await expect(page.getByTestId("switch-org-test2")).toContainText(
      "Test Org 2",
    );
    await expect(page.getByTestId("switch-org-test3")).toContainText(
      "Test Org 3",
    );
  });

  test("should switch organization when clicking org in dropdown", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Open user dropdown and click test2
    await page.getByTestId("user-menu-button").click();
    await expect(page.getByTestId("switch-org-test2")).toBeVisible();
    await page.getByTestId("switch-org-test2").click();

    // Wait for navigation to new org
    await page.waitForURL(/\/orgs\/test2/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Verify sidebar header shows new org name
    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toContainText("Test Org 2");

    // Verify URL contains the new org
    expect(page.url()).toContain("orgs/test2");

    // Wait for any pending dropdown close animations and React reconciliation
    // before reopening the dropdown
    await page.waitForTimeout(500);

    // Open dropdown again -- should now show test and test3, not test2
    const userMenuButton = page.getByTestId("user-menu-button");
    await expect(userMenuButton).toBeVisible();
    await userMenuButton.click();

    // Verify dropdown actually opened by checking for a known item
    await expect(page.getByTestId("logout-button")).toBeVisible({
      timeout: 5000,
    });

    await expect(
      page.getByTestId("switch-org-test3"),
    ).toBeVisible({ timeout: 5000 });
    // Verify current org (test2) is not in the switcher
    await expect(page.getByTestId("switch-org-test2")).not.toBeVisible();
  });

  test("should show fallback icon when user has no avatar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    const userMenuButton = page.getByTestId("user-menu-button");
    await expect(userMenuButton).toBeVisible();

    // Should NOT have an img element (test user has no avatar)
    const avatarImg = userMenuButton.locator("img");
    await expect(avatarImg).not.toBeVisible();

    // Should have the fallback icon container. Located by test id, not by
    // its fill: the chip moved off bg-muted when the sidebar went always-dark
    // (spec 2026-09-24-02), where the dark --muted is ~1:1 on the navy.
    const fallbackIcon = userMenuButton.getByTestId("user-avatar-fallback");
    await expect(fallbackIcon).toBeVisible();
  });
});

// Spec 2026-09-16-07 replaced a flat 14-item menu with four labelled groups.
// What these pin: the labels actually render, the Administration group is
// gated (Organization for an admin, nothing at all for a plain member), and
// the group labels never cost the icon rail its items — the one thing the
// SidebarGroupLabel CSS could break.
const GROUP_LABELS = ["Monitoring", "Alerting", "Public status", "Administration"];

const MEMBER_ITEMS = [
  "Dashboard",
  "Checks",
  "Incidents",
  "Events",
  "SLOs",
  "Integrations",
  "On-call",
  "Escalation policies",
  "My alerts",
  "Status Pages",
  "Updates & notices",
  "Maintenance",
];

/** Creates a user, an org they own, and a plain "user"-role member of it, then
 * logs THAT member in through the UI. Returns the org slug. The seeded
 * test@test.com is a super admin everywhere, so a genuinely non-admin session
 * has to be built. */
async function loginAsPlainMember(page: Page): Promise<string> {
  const stamp = uniqueStamp();
  const ownerEmail = `nav-owner-${stamp}@unknown.example`;
  const memberEmail = `nav-member-${stamp}@unknown.example`;
  const password = "Strong-Pass-123!";

  for (const [email, name] of [
    [ownerEmail, "Alice Owner"],
    [memberEmail, "Bob Member"],
  ]) {
    const resp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: { email, password, name },
    });
    if (resp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${resp.status()}`,
      );
    }
  }

  const ownerLogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email: ownerEmail, password },
  });
  expect(ownerLogin.status()).toBe(200);
  const ownerSession = (await ownerLogin.json()) as { accessToken: string };

  const orgSlug = `nav-${stamp}`;
  const createOrg = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${ownerSession.accessToken}` },
    data: { name: `Acme Nav ${stamp}`, slug: orgSlug },
  });
  expect(createOrg.status()).toBe(201);
  const org = (await createOrg.json()) as { accessToken: string };

  const addMember = await page.request.post(
    `${API_BASE}/api/v1/orgs/${orgSlug}/members`,
    {
      headers: { Authorization: `Bearer ${org.accessToken}` },
      data: { email: memberEmail, role: "user" },
    },
  );
  expect(addMember.status()).toBe(201);

  await page.goto(`${DASH_BASE}/orgs/${orgSlug}/login`);
  await page.getByTestId("login-email").fill(memberEmail);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("login"));

  return orgSlug;
}

test.describe("Sidebar navigation groups", () => {
  test("renders the four group labels", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    const sidebar = page.getByTestId("app-sidebar");
    for (const label of GROUP_LABELS) {
      await expect(
        sidebar.locator('[data-sidebar="group-label"]', { hasText: label }),
      ).toHaveCount(1);
    }
  });

  test("every grouped item is reachable, and the dropped entries are gone", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    const sidebar = page.getByTestId("app-sidebar");
    for (const item of MEMBER_ITEMS) {
      await expect(sidebar.getByRole("link", { name: item, exact: true })).toBeVisible();
    }

    // Badges moved under its check (08), Discovery under Organization (09),
    // and the Dependencies page is gone (10) — none of them belongs here.
    for (const gone of ["Badges", "Dependencies", "Discovery"]) {
      await expect(
        sidebar.getByRole("link", { name: gone, exact: true }),
      ).toHaveCount(0);
    }
  });

  test("Organization is in the sidebar for an admin", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByTestId("app-sidebar").getByRole("link", { name: "Organization", exact: true }),
    ).toBeVisible();
  });

  test("Organization and the whole Administration group are absent for a plain member", async ({
    page,
  }) => {
    const orgSlug = await loginAsPlainMember(page);
    await page.goto(`${DASH_BASE}/orgs/${orgSlug}`);
    await page.waitForLoadState("networkidle");

    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();
    // The member groups are all there …
    await expect(sidebar.getByRole("link", { name: "Checks", exact: true })).toBeVisible();
    // … and the admin one is not — no link, and no orphan label either.
    await expect(
      sidebar.getByRole("link", { name: "Organization", exact: true }),
    ).toHaveCount(0);
    await expect(sidebar.getByRole("link", { name: /^jobs$/i })).toHaveCount(0);
    await expect(
      sidebar.locator('[data-sidebar="group-label"]', { hasText: "Administration" }),
    ).toHaveCount(0);
  });

  test("the icon rail keeps every item while the group labels fade out", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    const sidebar = page.getByTestId("app-sidebar");
    await expect(sidebar).toBeVisible();

    const expandedBox = await sidebar.boundingBox();
    expect(expandedBox).not.toBeNull();

    // AppSidebar renders the default `collapsible="offcanvas"`, so the icon
    // rail is not reachable by clicking the trigger. Drive the exact data
    // attributes the shipped CSS keys off instead — this asserts the real
    // `group-data-[collapsible=icon]` rules in SidebarGroupLabel, which are
    // what a group label could break, rather than a stand-in for them.
    await page.evaluate((testid) => {
      const container = document.querySelector(`[data-testid="${testid}"]`);
      const group = container?.closest('[data-slot="sidebar"].group');
      group?.setAttribute("data-state", "collapsed");
      group?.setAttribute("data-collapsible", "icon");
    }, "app-sidebar");

    // The container really is the narrow rail now — everything below is
    // measured against it, so if this width never shrank the containment
    // assertions would be vacuous.
    // The width is animated (`transition-[left,right,width] duration-200`), so
    // wait for it to SETTLE at --sidebar-width-icon (3rem). Sampling the box
    // straight after the attribute flip catches a mid-animation ~170px and
    // makes every containment check below far looser than the real rail.
    await expect(sidebar).toHaveCSS("width", "48px");

    const rail = await sidebar.boundingBox();
    expect(rail).not.toBeNull();
    expect(rail!.width).toBeLessThan(expandedBox!.width);
    const railWidth = rail!.width;

    // Labels fade away — BOTH halves of the rule, because `opacity: 0` alone
    // would leave each label still occupying its `h-8` of vertical space and
    // pushing the items below it down the rail.
    for (const label of GROUP_LABELS) {
      const groupLabel = sidebar.locator('[data-sidebar="group-label"]', { hasText: label });
      await expect(groupLabel).toHaveCSS("opacity", "0");
      await expect(groupLabel).toHaveCSS("margin-top", "-32px");
      await expect(groupLabel).toHaveCSS("height", "32px");
    }

    // … and every item survives, visible and INSIDE the rail. A bounding box
    // is returned for clipped and off-screen elements too, so "not null" would
    // pass even with the items shoved out of the 3rem container: the check
    // that matters is that each item's box sits within the rail's bounds,
    // horizontally and vertically.
    for (const item of MEMBER_ITEMS) {
      const link = sidebar.getByRole("link", { name: item, exact: true });
      await expect(link).toBeVisible();

      const box = await link.boundingBox();
      expect(box, `${item} has no box on the icon rail`).not.toBeNull();
      expect(box!.width, `${item} has no width on the icon rail`).toBeGreaterThan(0);
      expect(box!.height, `${item} has no height on the icon rail`).toBeGreaterThan(0);

      // Allow a pixel of rounding slack on each edge, nothing more.
      expect(box!.x, `${item} starts left of the rail`).toBeGreaterThanOrEqual(rail!.x - 1);
      expect(
        box!.x + box!.width,
        `${item} overflows the ${railWidth}px rail horizontally`,
      ).toBeLessThanOrEqual(rail!.x + railWidth + 1);
      expect(box!.y, `${item} sits above the rail`).toBeGreaterThanOrEqual(rail!.y - 1);
      expect(
        box!.y + box!.height,
        `${item} is pushed below the bottom of the rail`,
      ).toBeLessThanOrEqual(rail!.y + rail!.height + 1);
    }
  });
});
