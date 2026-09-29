import { test, expect, API_BASE, escapeRegExp, type Page } from "./fixtures";

/**
 * Super-admin impersonation (spec 2026-09-29-03), against the REAL backend:
 * the test-mode super admin (test@test.com) views the dashboard as a freshly
 * seeded member, sees the non-dismissible banner, and exits back to their own
 * session. A normal member never sees the button.
 */

const PASSWORD = "Strong-Pass-123!";

async function seedMember(page: Page, email: string): Promise<void> {
  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password: PASSWORD, name: "Impersonation Target" },
  });
  if (created.status() !== 201) {
    test.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${created.status()}`,
    );
  }

  const added = await page.request.post(`${API_BASE}/api/v1/orgs/test/members`, {
    data: { email, role: "user" },
  });
  expect(added.status()).toBe(201);
}

async function findRow(page: Page, email: string) {
  await page.goto("orgs/test/server/users");
  await page.waitForLoadState("networkidle");
  // Wait for the SEARCHED page, not just any quiet moment: the search is
  // debounced, and a row found on the unfiltered first page is replaced by a
  // loader (unmounting any dialog opened from it) once the debounce fires.
  const searched = page.waitForResponse(
    (resp) =>
      resp.url().includes("/api/v1/system/users?") &&
      new URL(resp.url()).searchParams.get("q") === email,
  );
  await page.getByTestId("users-search").fill(email);
  await searched;
  await page.waitForLoadState("networkidle");

  const row = page.getByRole("row", { name: new RegExp(escapeRegExp(email)) });
  await expect(row).toBeVisible();

  return row;
}

test.describe("Super-admin impersonation", () => {
  test("a super admin views the dashboard as a member, then exits", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const email = `impersonate-${Date.now()}@example.test`;
    await seedMember(page, email);

    const row = await findRow(page, email);
    await row.getByRole("button", { name: "View as this user" }).click();

    const dialog = page.getByTestId("impersonate-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(email);
    await expect(dialog).toContainText("30 minutes");

    await page.getByTestId("impersonate-confirm").click();

    // Lands in the member's org, as the member, with the banner.
    await page.waitForURL((url) => !url.pathname.includes("/server/users"));
    const banner = page.getByTestId("impersonation-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(email);

    // The token never reaches the URL.
    expect(page.url()).not.toContain("accessToken");

    // The banner follows the admin across pages.
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("impersonation-banner")).toBeVisible();

    // Super-admin pages are closed to the impersonation.
    const me = await page.evaluate(async (base) => {
      const raw = sessionStorage.getItem("solidping_impersonation");
      const token = raw ? (JSON.parse(raw) as { accessToken: string }).accessToken : "";
      const resp = await fetch(`${base}/api/v1/system/users`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      return resp.status;
    }, API_BASE);
    expect(me).toBe(403);

    // Exit returns to the admin's own session, on the page they came from.
    await page.getByTestId("impersonation-exit").click();
    await page.waitForURL(/\/orgs\/test\/server\/users/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("impersonation-banner")).toHaveCount(0);
    await expect(page.getByTestId("users-table")).toBeVisible();
  });

  test("a normal member never sees the impersonate button", async ({
    authenticatedPage,
    browser,
  }) => {
    const email = `impersonate-plain-${Date.now()}@example.test`;
    await seedMember(authenticatedPage, email);

    // The super admin does not get the button on their own row.
    const selfRow = await findRow(authenticatedPage, "test@test.com");
    await expect(selfRow.getByRole("button", { name: "View as this user" })).toHaveCount(0);

    const context = await browser.newContext();
    const page = await context.newPage();

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("login-email").fill(email);
    await page.getByTestId("login-password").fill(PASSWORD);
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("button", { name: "View as this user" })).toHaveCount(0);
    await expect(page.getByTestId("users-table")).toHaveCount(0);

    await context.close();
  });
});
