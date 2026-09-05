import { test, expect, API_BASE } from "./fixtures";

// Covers the 2026-07-08 "create-org missing org-scoped token" fix: a fresh
// user with zero org memberships lands on /no-org, creates their first org,
// and must land on a WORKING /orgs/$org dashboard — not one where every
// org-scoped API call 403s because the frontend never adopted the fresh
// token the backend mints for the new org.
//
// A zero-org session is provisioned via the test-only
// `POST /api/v1/test/users` endpoint (SP_RUNMODE=test only, wired in
// server.go next to `/test/state-entries`) instead of the real
// register -> confirm-registration flow: this environment's auth.Service
// never actually picks up a live `auth.registration_email_pattern` system
// parameter (a separate, pre-existing, out-of-scope config-propagation bug),
// so `POST /auth/register` always returns "Registration is not enabled"
// here regardless of what's configured — see
// membership-requests.spec.ts's register-based tests, which hit the same
// wall and skip cleanly. Seeding the user directly and then logging in for
// real exercises the exact same zero-membership code path a genuine
// registered-but-orgless user would hit (`resolveOrgPreference` ->
// `LoginActionNoOrg` -> `completeLogin`'s `resolvedOrg == nil` branch in
// server/internal/handlers/auth/service.go), so the fix under test still
// gets full, real coverage.
test.describe("Create org from /no-org", () => {
  test("fresh zero-org user creates an org and lands on a working dashboard with no 403s", async ({
    page,
  }) => {
    const stamp = Date.now();
    const email = `create-org-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Create Org User" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    // Real login (no org preference passed) — with zero memberships the
    // auth service resolves LoginActionNoOrg and mints an org-scoped-to-
    // nothing access token, exactly like a real user who was removed from
    // their last org. This is the "no-org token" the create-org fix needs
    // to move past.
    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password },
    });
    expect(loginResp.status()).toBe(200);

    const session = (await loginResp.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      organization?: { slug: string };
    };
    expect(session.accessToken).toBeTruthy();
    expect(session.organization).toBeFalsy();

    // Pre-existing, unrelated bug: GET /api/v1/auth/me 401s for a
    // legitimate zero-org token (GetUserInfo unconditionally resolves
    // claims.OrgSlug via GetOrganizationBySlug, which errors for ""), and
    // AuthContext's validateSession() clears the session on that 401. Stub
    // /me for the initial app mount only (same technique the existing
    // "/no-org screen exposes both create and join cards" e2e test already
    // uses for the same reason) so the seeded real zero-org session
    // survives long enough to drive the actual create-org flow; unrouted
    // right after so the create-org POST and everything downstream hits
    // the real backend for real.
    await page.route("**/api/v1/auth/me", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ user: { email, role: "" }, organizations: [] }),
      }),
    );

    // Seed the browser with the confirmed (org-less) session before the app
    // loads — mirrors confirm-registration-handoff.ts's setSession call
    // (accessToken + refreshToken + expiresIn, all three — see
    // api/client.ts's setSession doc comment on why expiresIn/expiresAt
    // matter: without it validateSession treats this as a legacy/partial
    // session and forces an up-front refresh that fails for a zero-org
    // token, which has no refresh token by design).
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem(
            "solidping_refresh_token",
            refreshToken as string,
          );
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
      },
    );

    await page.goto("no-org");
    await page.waitForLoadState("networkidle");
    await page.unroute("**/api/v1/auth/me");

    const forbiddenUrls: string[] = [];
    const unauthorizedUrls: string[] = [];
    page.on("response", (response) => {
      if (response.status() === 403) forbiddenUrls.push(response.url());
      if (response.status() === 401) unauthorizedUrls.push(response.url());
    });

    await expect(page.getByTestId("no-org-advanced-toggle")).toBeVisible();

    const orgName = `Create Org Co ${stamp}`;
    const orgSlug = `e2e-${stamp.toString(36)}`;

    await page.locator("#orgName").fill(orgName);
    await page.getByTestId("no-org-advanced-toggle").click();
    await page.locator("#orgSlug").fill(orgSlug);

    await page.getByRole("button", { name: /create organization/i }).click();

    await page.waitForURL((url) => url.pathname.includes(`/orgs/${orgSlug}`), {
      timeout: 15000,
    });
    await page.waitForLoadState("networkidle");

    // The new org-scoped session must have been adopted — no "access
    // denied" screen, and no 401/403 anywhere along the way (create-org or
    // the dashboard's own API calls).
    await expect(
      page.getByText(/access to this organization is denied/i),
    ).toHaveCount(0);
    expect(
      forbiddenUrls,
      `unexpected 403s: ${forbiddenUrls.join(", ")}`,
    ).toEqual([]);
    expect(
      unauthorizedUrls,
      `unexpected 401s: ${unauthorizedUrls.join(", ")}`,
    ).toEqual([]);

    // Confirm the token that actually landed in storage is the NEW
    // org-scoped one, not the pre-creation no-org token.
    const storedToken = await page.evaluate(() =>
      localStorage.getItem("solidping_session_token"),
    );
    expect(storedToken).toBeTruthy();
    expect(storedToken).not.toBe(session.accessToken);
  });
});

// Spec 2026-09-05-01: a freshly created account that no org adopted must be
// OFFERED an organization, not steered toward the platform `default` one.
// These two tests are the UI half of it — the create form arrives pre-filled,
// "join an existing organization" is demoted to a disclosure, `default` appears
// nowhere, and Create works without the user typing anything at all.
test.describe("/no-org proposes an organization to a fresh account", () => {
  // Seeds a zero-org identity and lands its browser on /no-org, exactly the way
  // the create-org test above does (same test-only user endpoint, same /auth/me
  // stub for the initial mount only — see its comment block for why both are
  // needed here).
  async function landOnNoOrg(
    page: import("@playwright/test").Page,
    name: string | undefined,
    email: string,
  ) {
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, ...(name ? { name } : {}) } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password },
    });
    expect(loginResp.status()).toBe(200);

    const session = (await loginResp.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      organization?: { slug: string };
    };
    expect(session.organization).toBeFalsy();

    await page.route("**/api/v1/auth/me", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          user: { email, name, role: "" },
          organizations: [],
        }),
      }),
    );

    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem(
            "solidping_refresh_token",
            refreshToken as string,
          );
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
      },
    );

    await page.goto("no-org");
    await page.waitForLoadState("networkidle");
    await page.unroute("**/api/v1/auth/me");

    return session;
  }

  test("a named fresh user gets a possessive proposal, a secondary join card, and no mention of default", async ({
    page,
  }) => {
    const stamp = Date.now().toString(36);
    const firstName = `Alice${stamp}`;

    await landOnNoOrg(
      page,
      `${firstName} Newcomer`,
      `fresh-named-${stamp}@unknown.example`,
    );

    // 1. The create form arrives pre-filled with the possessive form built from
    //    the user's FIRST name (en: "Alice's organization").
    const orgName = page.locator("#orgName");
    await expect(orgName).toHaveValue(new RegExp(firstName));

    // 2. The join path is present but secondary: its trigger is visible, its
    //    form is not until you ask for it.
    const joinToggle = page.getByTestId("no-org-join-toggle");
    await expect(joinToggle).toBeVisible();
    await expect(page.locator("#joinOrgSlug")).toBeHidden();
    await joinToggle.click();
    await expect(page.locator("#joinOrgSlug")).toBeVisible();
    // ...and it never hints at an org — the placeholder stays the generic one.
    await expect(page.locator("#joinOrgSlug")).toHaveAttribute(
      "placeholder",
      "acme",
    );

    // 3. The word `default` appears nowhere on this screen. This is the whole
    //    point of the backend half of the spec: a brand-new SaaS account must
    //    never be told it is waiting on the operator's own organization.
    const bodyText = await page.locator("body").innerText();
    expect(bodyText).not.toMatch(/\bdefault\b/i);

    // 4. Create with NOTHING typed lands on a working dashboard — the server
    //    derives the slug from the proposed name (POST /api/v1/orgs with no
    //    `slug`).
    const forbiddenUrls: string[] = [];
    page.on("response", (response) => {
      if (response.status() === 403) forbiddenUrls.push(response.url());
    });

    await page.getByTestId("create-org-submit").click();

    await page.waitForURL((url) => /\/orgs\/[^/]+/.test(url.pathname), {
      timeout: 15000,
    });
    await page.waitForLoadState("networkidle");

    const landedSlug = new URL(page.url()).pathname
      .split("/orgs/")[1]
      ?.split("/")[0];
    expect(landedSlug).toBeTruthy();
    expect(landedSlug).not.toBe("default");
    expect(landedSlug).toContain(firstName.toLowerCase());

    await expect(
      page.getByText(/access to this organization is denied/i),
    ).toHaveCount(0);
    expect(
      forbiddenUrls,
      `unexpected 403s: ${forbiddenUrls.join(", ")}`,
    ).toEqual([]);
  });

  test("an unnamed fresh user still gets a non-empty proposal", async ({
    page,
  }) => {
    const stamp = Date.now().toString(36);

    // Registration's `name` is optional, so this is a real shape: no name at
    // all. The proposal must fall back to the random friendly word list rather
    // than shipping an empty form.
    await landOnNoOrg(
      page,
      undefined,
      `fresh-unnamed-${stamp}@unknown.example`,
    );

    const value = await page.locator("#orgName").inputValue();
    expect(value.trim().length).toBeGreaterThan(0);
    expect(value).not.toContain("createOrg.");
    expect(value.trim().split(/\s+/)).toHaveLength(2);

    // The slug preview shows what the server will derive, and it is not the
    // platform default.
    const bodyText = await page.locator("body").innerText();
    expect(bodyText).not.toMatch(/\bdefault\b/i);
  });
});
