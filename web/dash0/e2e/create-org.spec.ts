import { test, expect, API_BASE } from "./fixtures";

// Covers the 2026-07-08 "create-org missing org-scoped token" fix: a fresh
// user with zero org memberships lands on /no-org, creates their first org,
// and must land on a WORKING /orgs/$org dashboard — not one where every
// org-scoped API call 403s because the frontend never adopted the fresh
// token the backend mints for the new org.
//
// A brand-new zero-org session can only be produced through the real
// register -> confirm-registration flow (login always resolves to an
// existing org/no-org action based on memberships). Confirmation normally
// requires an emailed token; in SP_RUNMODE=test the pending token is
// readable back via the test-only `/api/v1/test/state-entries` endpoint
// instead of intercepting real email delivery.
test.describe("Create org from /no-org", () => {
  test("fresh zero-org user creates an org and lands on a working dashboard with no 403s", async ({
    page,
  }) => {
    const stamp = Date.now();
    const email = `create-org-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    const registerResp = await page.request.post(
      `${API_BASE}/api/v1/auth/register`,
      { data: { email, password, name: "Create Org User" } },
    );
    if (registerResp.status() !== 200 && registerResp.status() !== 201) {
      test.skip(true, `registration unavailable: ${registerResp.status()}`);
    }

    const entriesResp = await page.request.get(
      `${API_BASE}/api/v1/test/state-entries?prefix=email_registration:`,
    );
    if (entriesResp.status() !== 200) {
      test.skip(
        true,
        `test state-entries endpoint unavailable (server not in SP_RUNMODE=test?): ${entriesResp.status()}`,
      );
    }

    const entriesBody = (await entriesResp.json()) as {
      data: Array<{ Value?: { email?: string; token?: string } }>;
    };
    const entry = entriesBody.data.find((e) => e.Value?.email === email);
    expect(entry, "registration state entry for the new user").toBeTruthy();

    const confirmToken = entry?.Value?.token;
    expect(confirmToken).toBeTruthy();

    const confirmResp = await page.request.post(
      `${API_BASE}/api/v1/auth/confirm-registration`,
      { data: { token: confirmToken } },
    );
    expect(confirmResp.status()).toBe(200);

    const session = (await confirmResp.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      organization?: { slug: string };
    };
    expect(session.accessToken).toBeTruthy();
    // Zero org memberships: confirm-registration resolves no org — this is
    // the "no-org token" the create-org fix needs to move past.
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
          localStorage.setItem("solidping_refresh_token", refreshToken as string);
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
    expect(forbiddenUrls, `unexpected 403s: ${forbiddenUrls.join(", ")}`).toEqual(
      [],
    );
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
