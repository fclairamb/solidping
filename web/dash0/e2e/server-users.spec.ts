import { test, expect } from "./fixtures";

/**
 * E2E for the superadmin user directory (spec 2026-09-19-04).
 *
 * Unlike server-entitlements.spec.ts this one hits the REAL backend rather
 * than stubbing the API: the fixture data seeded for SP_RUNMODE=test already
 * gives us a super admin (test@test.com) with a real, known org membership,
 * which is exactly what the search/clear round trip needs to prove — a stub
 * would only prove the UI renders whatever JSON it is handed.
 */

test.describe("Superadmin user directory", () => {
  test("the Users tab is visible to the test-mode super admin", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/server/entitlements");
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("link", { name: "Users" })).toBeVisible();
  });

  test("lists test@test.com with an org badge for the test org", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");

    // test@test.com is the OLDEST seeded user, ordered created_at DESC — on
    // a suite with many users created after it, it can be several pages deep
    // on an unsearched first page. Search for it directly instead of relying
    // on it happening to land on page 1: that is what proves both that the
    // row renders AND that search actually finds it.
    await page.getByTestId("users-search").fill("test@test.com");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("users-table")).toBeVisible();

    const row = page.getByRole("row", { name: /test@test\.com/ });
    await expect(row).toBeVisible();
    // test@test.com belongs to THREE orgs (test, test2, test3), so a bare
    // "test" substring match would also pass on the test2/test3 badges.
    // Assert the "test" org's specific badge text ("{slug} · {role}").
    await expect(row.getByText("test · admin", { exact: true })).toBeVisible();
  });

  test("typing a non-matching query empties the table, and clearing it restores the row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");

    const search = page.getByTestId("users-search");

    // Reach the row via search first (see the test above for why an
    // unsearched first page cannot be relied on to contain it).
    await search.fill("test@test.com");
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("row", { name: /test@test\.com/ }),
    ).toBeVisible();

    // A non-matching query empties the table — the negative control that
    // proves the row above was found BY the search, not merely present.
    await search.fill("this-query-matches-absolutely-nobody-xyz");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("row", { name: /test@test\.com/ }),
    ).toHaveCount(0);
    await expect(page.getByTestId("users-table")).toHaveCount(0);

    // Clearing the search restores a real table, not just an empty one.
    await search.fill("");
    await page.waitForLoadState("networkidle");

    const table = page.getByTestId("users-table");
    await expect(table).toBeVisible();
    await expect(table.getByRole("row")).not.toHaveCount(0);

    // And searching again reaches the same specific row.
    await search.fill("test@test.com");
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("row", { name: /test@test\.com/ }),
    ).toBeVisible();
  });

  test("an API 403 renders Permission Denied in place, never a redirect", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // The client still believes it is a superadmin (the layout gate passes);
    // the SERVER says no. That combination is the one that used to loop.
    await page.route("**/api/v1/system/users*", async (route) => {
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({
          title: "Super admin access required",
          code: "FORBIDDEN",
        }),
      });
    });

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Permission Denied")).toBeVisible();
    expect(page.url()).toContain("/server/users");
    expect(page.url()).not.toContain("/login");
  });

  test("a non-superadmin never reaches the directory", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.route("**/api/v1/auth/me", async (route) => {
      const response = await route.fetch();
      const body = await response.json();

      // isSuperAdmin is derived from role === "superadmin" (AuthContext), so
      // downgrading the role is what actually drops the capability.
      if (body?.user) {
        body.user.role = "admin";
      }

      await route.fulfill({
        status: response.status(),
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    });

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");

    // The Server layout's own gate keeps the whole area away from a
    // non-superadmin; what matters here is that the directory never renders.
    await expect(page.getByTestId("users-search")).toHaveCount(0);
    expect(page.url()).not.toContain("/login");
  });
});
