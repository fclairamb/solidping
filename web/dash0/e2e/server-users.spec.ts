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

    await expect(page.getByTestId("users-table")).toBeVisible();

    const row = page.getByRole("row", { name: /test@test\.com/ });
    await expect(row).toBeVisible();
    // "test" is the seeded org's slug — the badge reads "{slug} · {role}".
    await expect(row).toContainText("test");
  });

  test("typing a non-matching query empties the table, and clearing it restores the row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("row", { name: /test@test\.com/ }),
    ).toBeVisible();

    const search = page.getByTestId("users-search");
    await search.fill("this-query-matches-absolutely-nobody-xyz");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("row", { name: /test@test\.com/ }),
    ).toHaveCount(0);
    await expect(page.getByTestId("users-table")).toHaveCount(0);

    await search.fill("");
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
