import { test, expect, API_BASE } from "./fixtures";

// Regression e2e for spec 2026-08-29-06: confirming a brand-new
// email+password registration whose email matches no org's auto-join
// pattern used to come back from the backend with no access token at all.
// The frontend then persisted the literal string "undefined" as the
// session, and /no-org's first authenticated call 401'd the user straight
// back to /login — right after their account was actually created (the
// perceived state was "registration failed", and retrying then failed
// again with email-taken).
//
// Registration is disabled by default (auth.registration_email_pattern
// unset); this suite's side-car test server must be started with
// SP_AUTH_REGISTRATION_EMAIL_PATTERN=".*" for this test to be able to
// drive the real register -> confirm-registration flow, rather than the
// `POST /test/users` shortcut other specs use. That shortcut exists
// specifically because PUT-ing the system parameter at *runtime* does NOT
// take effect (see create-org.spec.ts's comment): systemconfig's DB
// overlay onto the live config is applied once, at process boot, so only
// an env var set before the server starts actually enables registration
// for this flow.
test.describe("Confirm registration with no matching org", () => {
  test("register -> confirm lands the user on /no-org still authenticated, no bounce to login", async ({
    page,
  }) => {
    const stamp = Date.now();
    const email = `confirm-no-org-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";

    // Pre-existing, unrelated bug (found while writing this test, not part
    // of spec 2026-08-29-06): OrgLayout ($org.tsx) calls useFeatures()
    // unconditionally, even for the public login/register pages
    // (isLoginPage is computed right above it and never used to gate the
    // call). GET /api/v1/features requires auth, so an unauthenticated
    // visitor to /orgs/:org/register 401s on that background call and gets
    // silently bounced to /login?session_expired=true before they can even
    // see the form — redirectToExpiredLogin() only no-ops when the CURRENT
    // path already ends in "/login", not for other public pages. Stubbed
    // here the same way bug-report.spec.ts stubs this endpoint; a separate
    // task flags the underlying bug.
    await page.route("**/api/v1/features", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ bugReport: false }),
      }),
    );

    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");

    await page.locator("#name").fill("Confirm No Org User");
    await page.locator("#email").fill(email);
    await page.locator("#password").fill(password);
    await page.getByRole("button", { name: "Create account" }).click();

    // "Check your email" success screen renders the address back.
    await expect(page.getByText(email)).toBeVisible({ timeout: 10000 });

    // Fetch the one-shot confirmation token the same way the backend unit
    // test does (test-mode-only introspection endpoint) instead of a real
    // mailbox. StateEntry has no json tags, so field names are the bare Go
    // field names (Key, Value), while Value's own keys are the lowercase
    // ones the auth service writes ("email", "token" — see
    // server/internal/handlers/auth/service.go's keyEmail/keyToken).
    const stateResp = await page.request.get(
      `${API_BASE}/api/v1/test/state-entries?prefix=email_registration:`,
    );
    expect(stateResp.status()).toBe(200);
    const { data: entries } = (await stateResp.json()) as {
      data: Array<{ Value: Record<string, unknown> | null }>;
    };

    const entry = entries.find((e) => e.Value?.email === email);
    expect(entry, "the registration state entry must exist").toBeTruthy();
    const token = entry?.Value?.token as string;
    expect(token).toBeTruthy();

    await page.goto(`confirm-registration/${token}`);
    await page.waitForLoadState("networkidle");

    // The "confirmed" screen flashes, then the route auto-navigates after
    // 1.5s — wait for the real destination, not the flash.
    await page.waitForURL(/\/no-org(\?|$)/, { timeout: 10000 });

    // Must NOT have bounced to login — the bug this spec fixes.
    expect(page.url()).not.toContain("/login");
    expect(page.url()).not.toContain("session_expired");

    // The session must be a real token, not the literal string "undefined"
    // (the root cause this spec fixes at the source: client.ts's setSession
    // used to be handed `data.accessToken === undefined` and dutifully
    // persisted it).
    const storedToken = await page.evaluate(() =>
      window.localStorage.getItem("solidping_session_token"),
    );
    expect(storedToken).toBeTruthy();
    expect(storedToken).not.toBe("undefined");

    // And the page must actually be authenticated and working — /no-org's
    // create-org card only renders its form for a signed-in user with real
    // API access, not a stub/error state.
    await expect(page.getByTestId("no-org-advanced-toggle")).toBeVisible({
      timeout: 10000,
    });
  });
});
