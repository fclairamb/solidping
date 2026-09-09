import { test, expect, type Page } from "@playwright/test";
import {
  DASH_BASE,
  escapeRegExp,
  fetchAuthCapabilities,
  LAST_AUTH_METHOD_KEY,
} from "./fixtures";

/**
 * /register offers the same third-party sign-in buttons as /login
 * (spec 2026-09-09-02).
 *
 * Every provider callback runs `findOrCreateUser`, so a first-time "Continue
 * with GitHub" already creates the account — the buttons were simply missing
 * from the page whose whole job is creating one.
 *
 * Two of these tests prove negatives (no promoted "last used" slot, no passkey
 * button on /register). A negative that passes trivially is worthless, so each
 * one first asserts the corresponding POSITIVE on /login under the exact same
 * browser state — same seeded localStorage, same backend config — and only
 * then asserts its absence on /register.
 */

/** Stops the browser from actually leaving for the provider's consent screen. */
async function interceptProviderRedirects(page: Page): Promise<string[]> {
  const requested: string[] = [];
  await page.route("**/api/v1/auth/*/login*", async (route) => {
    requested.push(route.request().url());
    // Answer with a body rather than aborting: an aborted top-level
    // navigation leaves the page in an error state that later assertions
    // would have to work around.
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<html><body>provider stub</body></html>",
    });
  });
  return requested;
}

test.describe("Register: third-party auth buttons", () => {
  test("renders one button per configured provider, in the same order as /login", async ({
    page,
    baseURL,
  }) => {
    const { providers } = await fetchAuthCapabilities(baseURL);
    test.skip(
      providers.length === 0,
      "no OAuth provider configured on the test backend",
    );

    // Read the login page's order first — it is the reference the register
    // page must match, and it is the surface that already worked.
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    const loginOrder: string[] = [];
    for (const provider of providers) {
      await expect(
        page.getByTestId(`login-oauth-${provider.type}`),
      ).toBeVisible();
      loginOrder.push(provider.type);
    }

    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("button", { name: "Create account" }),
    ).toBeVisible();

    for (const provider of providers) {
      const button = page.getByTestId(`register-oauth-${provider.type}`);
      await expect(button).toBeVisible();
      // The bare provider name, exactly as on /login — no "Sign up with …".
      await expect(button).toHaveText(provider.name);
    }

    // Same order. Compare rendered DOM order, not just presence: the two
    // pages are fed by the same list and must not diverge.
    const renderedOrder = await page
      .locator("[data-testid^='register-oauth-']")
      .evaluateAll((nodes) =>
        nodes.map((node) =>
          (node.getAttribute("data-testid") ?? "").replace(
            "register-oauth-",
            "",
          ),
        ),
      );
    expect(renderedOrder).toEqual(loginOrder);

    // The buttons sit ABOVE the password form — they are the headline offer,
    // not an afterthought below the fields.
    const firstButton = await page
      .getByTestId(`register-oauth-${providers[0].type}`)
      .boundingBox();
    const emailBox = await page.locator("#email").boundingBox();
    expect(firstButton).not.toBeNull();
    expect(emailBox).not.toBeNull();
    expect(firstButton!.y).toBeLessThan(emailBox!.y);
  });

  test("clicking a provider button starts that provider's login with the org and redirect_uri", async ({
    page,
    baseURL,
  }) => {
    const { providers } = await fetchAuthCapabilities(baseURL);
    test.skip(
      providers.length === 0,
      "no OAuth provider configured on the test backend",
    );
    const provider = providers[0];

    const requested = await interceptProviderRedirects(page);

    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");
    await page.getByTestId(`register-oauth-${provider.type}`).click();

    await expect(() => expect(requested.length).toBeGreaterThan(0)).toPass({
      timeout: 10000,
    });

    const requestURL = new URL(requested[0]);
    expect(requestURL.pathname).toBe(`/api/v1/auth/${provider.type}/login`);
    expect(requestURL.searchParams.get("org")).toBe("test");
    // No returnTo on /register, so the callback lands on the org root.
    expect(requestURL.searchParams.get("redirect_uri")).toBe(
      `${DASH_BASE}/orgs/test`,
    );

    // The click is remembered, so the NEXT visit to /login promotes the
    // provider the visitor signed up with.
    const stored = await page.evaluate(
      (key) => window.localStorage.getItem(key),
      LAST_AUTH_METHOD_KEY,
    );
    expect(stored).toBe(`oauth:${provider.type}`);
  });

  test("never promotes a last-used provider on /register, even when /login does", async ({
    page,
    baseURL,
  }) => {
    const { providers } = await fetchAuthCapabilities(baseURL);
    test.skip(
      providers.length === 0,
      "no OAuth provider configured on the test backend",
    );
    const provider = providers[0];

    await page.addInitScript(
      ([key, value]) => {
        window.localStorage.setItem(key, value);
      },
      [LAST_AUTH_METHOD_KEY, `oauth:${provider.type}`],
    );

    // Positive control: with this exact storage state, /login DOES promote —
    // otherwise the assertions below would pass on a page where the feature
    // simply never fires.
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("login-last-used")).toBeVisible();
    await expect(
      page.getByTestId(`login-oauth-${provider.type}-promoted`),
    ).toBeVisible();

    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId(`register-oauth-${provider.type}`),
    ).toBeVisible();

    // A visitor on /register is claiming to be new: no promoted slot, no
    // "Last used" badge, and — the other half of the promotion logic — the
    // provider is NOT removed from the grid as a duplicate.
    await expect(page.getByTestId("login-last-used")).toHaveCount(0);
    await expect(page.getByTestId("login-last-used-badge")).toHaveCount(0);
    await expect(
      page.getByTestId(`register-oauth-${provider.type}-promoted`),
    ).toHaveCount(0);
    await expect(
      page.locator("[data-testid^='register-oauth-']"),
    ).toHaveCount(providers.length);
  });

  test("offers no passkey button on /register, even when /login does", async ({
    page,
    baseURL,
  }) => {
    const { passkeysEnabled } = await fetchAuthCapabilities(baseURL);
    test.skip(!passkeysEnabled, "passkeys are not enabled on the test backend");

    // Positive control on the same backend config: /login renders it.
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("passkey-login-button")).toBeVisible();

    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("button", { name: "Create account" }),
    ).toBeVisible();

    // Passkey enrolment is authenticated (/passkeys/register/begin|finish are
    // mounted on rootAuthProtected), so there is nothing a passkey could do
    // for someone without an account yet — and a button that lands on a LOGIN
    // ceremony would be a lie. Neither variant may appear.
    await expect(page.getByTestId("passkey-login-button")).toHaveCount(0);
    await expect(
      page.getByTestId("passkey-login-button-promoted"),
    ).toHaveCount(0);
  });

  test("stays on /register with the sign-up form intact", async ({ page }) => {
    // The buttons must not have disturbed the public-route behaviour the
    // register page already had: no 401 bounce, no session_expired redirect,
    // and the password fields still there below the grid.
    await page.goto("orgs/test/register");
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1000);

    await expect(page).toHaveURL(
      new RegExp(`${escapeRegExp(DASH_BASE)}/orgs/test/register`),
    );
    expect(new URL(page.url()).searchParams.get("session_expired")).toBeNull();
    await expect(page.locator("#email")).toBeVisible();
    await expect(page.locator("#password")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Create account" }),
    ).toBeVisible();
  });
});
