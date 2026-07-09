import { test, expect, API_BASE } from "./fixtures";

// Regression coverage for 2026-07-09 "Zero-org session gets logged out almost
// immediately": a user with zero org memberships lands on /no-org but the
// session used to die on the very next page load/reload — GET /api/v1/auth/me
// 401'd for the empty-org-slug token (GetUserInfo resolved claims.OrgSlug via
// GetOrganizationBySlug, which errored for ""), and AuthContext's
// validateSession() then cleared the session (either via the 401 catch, or via
// the up-front forced refresh that escalates because a zero-org token has no
// refresh token by design). Both are now fixed; these tests drive the REAL
// zero-org login (no stubbed /auth/me) and prove the session survives a reload.
//
// A zero-org session is provisioned via the test-only POST /api/v1/test/users
// endpoint (SP_RUNMODE=test only) exactly like create-org.spec.ts, then a real
// POST /auth/login with no org preference mints the LoginActionNoOrg token.

test.describe("Zero-org session survives", () => {
  // Seeds a real confirmed user with no org membership and logs them in for
  // real, returning the minted no-org session (access token, maybe refresh /
  // expiry). Skips the test cleanly if the test-only seed endpoint is absent.
  async function seedZeroOrgSession(page: import("@playwright/test").Page) {
    const stamp = Date.now();
    const email = `zero-org-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password, name: "Zero Org User" } },
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
    expect(session.accessToken).toBeTruthy();
    // A no-org login carries no organization and no refresh token by design.
    expect(session.organization).toBeFalsy();
    expect(session.refreshToken).toBeFalsy();

    return { email, session };
  }

  // Asserts, after the app has mounted and run validateSession(), that the
  // session was NOT destroyed: the token is still in storage, the real
  // /auth/me returned 200 (never 401), and we were not bounced to the
  // session-expired login screen.
  async function expectSessionSurvived(
    page: import("@playwright/test").Page,
    accessToken: string,
    meStatuses: number[],
  ) {
    // The real /auth/me was hit and answered 200 — never 401.
    expect(
      meStatuses.length,
      "expected at least one real GET /auth/me call",
    ).toBeGreaterThan(0);
    expect(
      meStatuses,
      `/auth/me must never 401 for a zero-org session, saw: ${meStatuses.join(", ")}`,
    ).not.toContain(401);

    // validateSession() did not clear the token (bug #2's catch path) nor did
    // the forced refresh escalate and wipe it.
    const storedToken = await page.evaluate(() =>
      localStorage.getItem("solidping_session_token"),
    );
    expect(storedToken, "session token must survive the reload").toBe(
      accessToken,
    );

    // We were not redirected to the session-expired login screen.
    expect(page.url()).not.toContain("session_expired");
    expect(page.url()).toContain("no-org");

    // The /no-org surface actually rendered its cards (the session is usable).
    await expect(
      page.getByRole("heading", { name: /start a new organization/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: /join an existing organization/i }),
    ).toBeVisible();
  }

  test("real zero-org login survives a /no-org reload (backend /auth/me fix)", async ({
    page,
  }) => {
    const { session } = await seedZeroOrgSession(page);

    // Seed the browser with the real no-org session the way the app itself
    // does: access token + expiry (from expiresIn), no refresh token. With
    // expires_at present, validateSession skips the up-front refresh and the
    // survival depends purely on /auth/me no longer 401ing.
    await page.addInitScript(
      ({ accessToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
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
        expiresIn: session.expiresIn ?? 0,
      },
    );

    const meStatuses: number[] = [];
    page.on("response", (response) => {
      if (response.url().includes("/api/v1/auth/me")) {
        meStatuses.push(response.status());
      }
    });

    // Real /auth/me — NOT stubbed (unlike create-org.spec.ts / the old
    // membership-requests test, which stubbed around this exact bug).
    await page.goto("no-org");
    await page.waitForLoadState("networkidle");
    // The reported bug fires on the next page load/reload — exercise it.
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expectSessionSurvived(page, session.accessToken, meStatuses);
  });

  test("legacy zero-org session (no expires_at) is not force-logged-out", async ({
    page,
  }) => {
    const { session } = await seedZeroOrgSession(page);

    // Seed ONLY the access token — no expires_at — so validateSession takes
    // the getExpiresAt()===null branch. A zero-org token has no refresh token,
    // so the old code called refreshWithOutcome() → escalate("no-refresh-
    // token") → clear + redirect to login?session_expired=true. The fix gates
    // that forced refresh on a refresh token actually existing, letting
    // /auth/me (200, no org) be the arbiter instead.
    await page.addInitScript(
      ({ accessToken }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
      },
      { accessToken: session.accessToken },
    );

    const meStatuses: number[] = [];
    page.on("response", (response) => {
      if (response.url().includes("/api/v1/auth/me")) {
        meStatuses.push(response.status());
      }
    });

    await page.goto("no-org");
    await page.waitForLoadState("networkidle");
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expectSessionSurvived(page, session.accessToken, meStatuses);
  });
});
