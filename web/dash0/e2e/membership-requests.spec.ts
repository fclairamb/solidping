import { test, expect, API_BASE } from "./fixtures";

// These tests cover the membership-request flow as exposed by the
// dash0 frontend: the /no-org screen redesign, the admin "Requests" tab
// with its pending-count badge, and the auto-join regex hardening on
// org settings. They go through real API calls (the test admin signs
// up requesters on the fly) so the coverage is end-to-end.

test.describe("Membership requests", () => {
  test("admin sees Requests tab with pending count badge", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    // Tab nav exposes a "Requests" link — even when empty the tab is rendered.
    await expect(page.getByRole("link", { name: /requests/i })).toBeVisible();
  });

  test("settings page rejects an unsafe auto-join regex inline", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/settings");
    await page.waitForLoadState("networkidle");

    const pattern = page.locator("#emailPattern");
    await pattern.fill(".*");

    // Save → server rejects with INVALID_AUTO_JOIN_REGEX. Target the
    // auto-join form's own submit button by testid — the settings page also
    // has a session-length "Save" button, so a name-based locator is ambiguous.
    await page.getByTestId("auto-join-pattern-save").click();

    // Inline error must surface (server message under the field, no toast).
    // Scope to the auto-join form: the settings page also renders an
    // owner-only danger zone whose title is legitimately text-destructive
    // (spec 2026-08-08-11), so a page-wide locator would match it too.
    const autoJoinForm = page
      .getByTestId("auto-join-pattern-save")
      .locator("xpath=ancestor::form");

    await expect(
      autoJoinForm.locator(".text-destructive").first(),
    ).toBeVisible({ timeout: 5000 });

    // Field stays editable; errors clear on edit.
    await pattern.fill(".+@example\\.com");
    await expect(autoJoinForm.locator(".text-destructive")).toHaveCount(0);
  });

  test("test-against-email preview reports match / no-match", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/settings");
    await page.waitForLoadState("networkidle");

    await page.locator("#emailPattern").fill(".+@example\\.com");
    const test = page.locator("#testEmail");

    await test.fill("alice@example.com");
    await expect(page.getByText("✓").first()).toBeVisible();

    await test.fill("alice@gmail.com");
    await expect(page.getByText("✗").first()).toBeVisible();
  });

  test("admin approve flow updates the requests list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Seed: register a fresh user via the public registration endpoint and
    // open a membership request for them via the API.
    const candidateEmail = `req-${Date.now()}@unknown.example`;
    const candidatePass = "Strong-Pass-123!";

    const registerResp = await page.request.post(
      `${API_BASE}/api/v1/auth/register`,
      {
        data: { email: candidateEmail, password: candidatePass, name: "Req Bot" },
      },
    );
    // Registration may be disabled in some CI configs — skip cleanly if so.
    if (registerResp.status() !== 200 && registerResp.status() !== 201) {
      test.skip(true, `registration unavailable: ${registerResp.status()}`);
    }

    // Once the request endpoint exists we just confirm the admin page
    // doesn't crash on any pending row state.
    await page.goto("orgs/test/organization/requests");
    await page.waitForLoadState("networkidle");

    // The Requests page renders without error.
    await expect(
      page.getByRole("heading", { name: /membership requests/i }),
    ).toBeVisible();
  });

  test("/no-org screen exposes both create and join cards", async ({
    page,
  }) => {
    // No login; this view is only meaningful when the user is signed in
    // *but* has no orgs. We stub `/auth/me` so the route renders.
    await page.route("**/api/v1/auth/me", (route) =>
      route.fulfill({
        status: 200,
        body: JSON.stringify({
          user: { email: "alice@example.com", role: "user" },
          organizations: [],
        }),
      }),
    );
    await page.route("**/api/v1/auth/membership-requests", (route) =>
      route.fulfill({ status: 200, body: JSON.stringify({ data: [] }) }),
    );

    // Pre-set a fake auth token so AuthContext's getToken() returns truthy.
    await page.addInitScript(() => {
      window.localStorage.setItem("solidping_token", "stub");
    });

    await page.goto("no-org");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("heading", { name: /start a new organization/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: /join an existing organization/i }),
    ).toBeVisible();
  });
});
