import { test, expect, API_BASE } from "./fixtures";

// Coverage for spec 2026-09-09-05: the members table's "Last seen" column,
// derived from two independent signals — a dashboard session
// (`lastSessionActivityAt`) and a credential like a PAT
// (`lastTokenActivityAt`) — that the API deliberately never pre-merges.
//
// Each test seeds its OWN member via the test-only `POST /api/v1/test/users`
// endpoint (SP_RUNMODE=test only) plus `POST /orgs/test/members`, rather than
// reusing the shared `test@test.com` identity: that account's own token
// activity is mutated by other suites running in the same worker (session
// refreshes, PAT tests, …), which would make "shows session, not token"
// assertions flaky. A fresh member has a clean, entirely-under-this-test's-
// control activity history.

const PASSWORD = "Strong-Pass-123!";

async function createTestUser(
  page: import("./fixtures").Page,
  email: string,
  password: string,
): Promise<void> {
  const resp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password, name: "Last Seen Test User" },
  });
  if (resp.status() !== 201) {
    test.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${resp.status()}`,
    );
  }
}

async function addAsMember(
  page: import("./fixtures").Page,
  email: string,
): Promise<void> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/members`,
    { data: { email, role: "user" } },
  );
  expect(resp.status()).toBe(201);
}

test.describe("Members last seen", () => {
  test("a member who logged in shows a relative time and the session icon", async ({
    authenticatedPage,
    browser,
  }) => {
    const page = authenticatedPage;
    const email = `last-seen-session-${Date.now()}@example.test`;

    await createTestUser(page, email, PASSWORD);
    await addAsMember(page, email);

    // Log the new member in for real, in an ISOLATED browser context so this
    // doesn't disturb the admin's own session cookie on `page`. A real
    // /auth/login call mints a `refresh` user_tokens row with
    // last_active_at = now (server/internal/handlers/auth/service.go).
    const memberContext = await browser.newContext();
    const memberPage = await memberContext.newPage();
    const loginResp = await memberPage.request.post(
      `${API_BASE}/api/v1/auth/login`,
      { data: { org: "test", email, password: PASSWORD } },
    );
    expect(loginResp.status()).toBe(200);
    await memberContext.close();

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    const lastSeenCell = page.getByTestId(`member-last-seen-${email}`);
    await expect(lastSeenCell).toBeVisible();
    // Never "Never" — evidence of an actual session, not the empty state.
    await expect(lastSeenCell).not.toContainText("Never");

    const viaIcon = page.getByTestId(`member-last-seen-via-${email}`);
    await expect(viaIcon).toBeVisible();
    await expect(viaIcon).toHaveAttribute("data-via", "session");
  });

  test("a member with both a session and a used PAT shows both channels in the breakdown tooltip", async ({
    authenticatedPage,
    browser,
  }) => {
    const page = authenticatedPage;
    const email = `last-seen-both-${Date.now()}@example.test`;

    await createTestUser(page, email, PASSWORD);
    await addAsMember(page, email);

    const memberContext = await browser.newContext();
    const memberPage = await memberContext.newPage();

    const loginResp = await memberPage.request.post(
      `${API_BASE}/api/v1/auth/login`,
      { data: { org: "test", email, password: PASSWORD } },
    );
    expect(loginResp.status()).toBe(200);
    const { accessToken } = (await loginResp.json()) as { accessToken: string };

    // Mint a PAT as this member (their own credential — the tokens endpoint
    // is self-scoped) and then use it, via the Bearer header rather than the
    // session cookie, to bump its last_active_at. The two events land
    // seconds apart, so the assertions below check the TOOLTIP CONTENTS
    // (both channel labels + non-"never" times), never which icon "won" —
    // that ordering is not deterministic sub-second.
    const createTokenResp = await memberPage.request.post(
      `${API_BASE}/api/v1/orgs/test/tokens`,
      {
        data: { name: "last-seen-e2e-pat" },
        headers: { Authorization: `Bearer ${accessToken}` },
      },
    );
    expect(createTokenResp.status()).toBe(201);
    const { token: patToken } = (await createTokenResp.json()) as { token: string };

    const useTokenResp = await memberPage.request.get(
      `${API_BASE}/api/v1/orgs/test/checks`,
      { headers: { Authorization: `Bearer ${patToken}` } },
    );
    expect(useTokenResp.status()).toBe(200);

    await memberContext.close();

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    const lastSeenCell = page.getByTestId(`member-last-seen-${email}`);
    await expect(lastSeenCell).toBeVisible();
    await expect(lastSeenCell).not.toContainText("Never");

    const viaIcon = page.getByTestId(`member-last-seen-via-${email}`);
    await expect(viaIcon).toBeVisible();

    await viaIcon.hover();
    const tooltip = page.getByRole("tooltip").filter({ hasText: "Dashboard" });
    await expect(tooltip).toBeVisible({ timeout: 5000 });
    await expect(tooltip).toContainText("Dashboard");
    await expect(tooltip).toContainText("API token");
    // Neither breakdown line is the "never" placeholder: both channels
    // genuinely happened.
    await expect(tooltip).not.toContainText("Never");
  });

  test("a member added via the API who never logged in shows Never and no icon", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const email = `last-seen-never-${Date.now()}@example.test`;

    await createTestUser(page, email, PASSWORD);
    await addAsMember(page, email);
    // Deliberately never logging this member in and never minting a token —
    // this is the negative control.

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    const lastSeenCell = page.getByTestId(`member-last-seen-${email}`);
    await expect(lastSeenCell).toBeVisible();
    // Anchored on the actual localized "Never" text (not a dash or blank),
    // so this would fail if the column silently rendered nothing instead.
    await expect(lastSeenCell).toHaveText("Never");

    await expect(
      page.getByTestId(`member-last-seen-via-${email}`),
    ).toHaveCount(0);
  });
});
