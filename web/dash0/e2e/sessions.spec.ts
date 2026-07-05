import { test, expect } from "./fixtures";

test.describe("Sessions", () => {
  test("lists the current session with the current-session badge", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/account/sessions");
    await page.waitForLoadState("networkidle");

    // The login the fixture just performed is this page's own session — it
    // must show up, and be flagged current.
    const currentBadge = page.getByTestId("session-current-badge");
    await expect(currentBadge).toBeVisible();

    const currentRow = currentBadge.locator(
      "xpath=ancestor::*[starts-with(@data-testid, 'session-row-')]"
    );
    await expect(currentRow).toBeVisible();
    // Exactly one row carries the badge.
    await expect(page.getByTestId("session-current-badge")).toHaveCount(1);
  });

  test("sign out other sessions deletes every session but the caller's own", async ({
    authenticatedPage,
    browser,
  }) => {
    const page = authenticatedPage;

    // Open a second, independent session (its own browser context, separate
    // cookie/localStorage jar) so there's an "other" session to sign out.
    const otherContext = await browser.newContext();
    const otherPage = await otherContext.newPage();
    await otherPage.goto("orgs/test/login");
    await otherPage.waitForLoadState("networkidle");
    await otherPage.getByTestId("login-title").waitFor({ state: "visible", timeout: 10000 });
    await otherPage.getByTestId("login-email").fill("test@test.com");
    await otherPage.getByTestId("login-password").fill("test");
    await otherPage.getByTestId("login-submit").click();
    await otherPage.waitForURL((url) => !url.pathname.includes("login"), {
      timeout: 10000,
    });
    await otherPage.waitForLoadState("networkidle");

    await page.goto("orgs/test/account/sessions");
    await page.waitForLoadState("networkidle");

    // At least the caller's own session plus the one just opened above.
    await expect(page.getByTestId("sign-out-others-button")).toBeEnabled();

    await page.getByTestId("sign-out-others-button").click();
    await page.getByTestId("sign-out-others-confirm").click();
    await page.waitForLoadState("networkidle");

    // Only the current session remains, and "sign out others" is now
    // disabled (nothing left to sign out).
    await expect(page.getByTestId("session-current-badge")).toHaveCount(1);
    await expect(page.getByTestId("sign-out-others-button")).toBeDisabled();

    // Revocation is a refresh-token-row delete, not an access-token
    // blocklist — the other context's already-issued access token stays
    // valid until it naturally expires (that's the acceptance criterion:
    // "its next refresh 401s", not "its next navigation"). Assert the
    // actual contract directly against the API: that session's refresh
    // token must now be rejected.
    const otherRefreshToken = await otherPage.evaluate(() =>
      window.localStorage.getItem("solidping_refresh_token")
    );
    expect(otherRefreshToken).toBeTruthy();

    const refreshResponse = await page.request.post("/api/v1/auth/refresh", {
      data: { refreshToken: otherRefreshToken },
    });
    expect(refreshResponse.status()).toBe(401);

    await otherContext.close();
  });

  test("revoking another session removes it from the list", async ({
    authenticatedPage,
    browser,
  }) => {
    const page = authenticatedPage;

    const otherContext = await browser.newContext();
    const otherPage = await otherContext.newPage();
    await otherPage.goto("orgs/test/login");
    await otherPage.waitForLoadState("networkidle");
    await otherPage.getByTestId("login-title").waitFor({ state: "visible", timeout: 10000 });
    await otherPage.getByTestId("login-email").fill("test@test.com");
    await otherPage.getByTestId("login-password").fill("test");
    await otherPage.getByTestId("login-submit").click();
    await otherPage.waitForURL((url) => !url.pathname.includes("login"), {
      timeout: 10000,
    });
    await otherPage.waitForLoadState("networkidle");

    await page.goto("orgs/test/account/sessions");
    await page.waitForLoadState("networkidle");

    const rowCountBefore = await page
      .locator("[data-testid^='session-row-']")
      .count();
    expect(rowCountBefore).toBeGreaterThanOrEqual(2);

    // Find a non-current row and revoke it.
    const otherRow = page
      .locator("[data-testid^='session-row-']")
      .filter({ hasNot: page.getByTestId("session-current-badge") })
      .first();
    await expect(otherRow).toBeVisible();
    await otherRow.locator("[data-testid^='session-revoke-button-']").click();
    await page.getByTestId("session-revoke-confirm").click();
    await page.waitForLoadState("networkidle");

    await expect(
      page.locator("[data-testid^='session-row-']")
    ).toHaveCount(rowCountBefore - 1);

    // Revocation invalidates the session's refresh token immediately — its
    // already-issued access token stays valid until natural expiry (that's
    // the acceptance criterion: "its next refresh 401s"), so assert the
    // refresh contract directly rather than expecting an instant
    // navigation-time redirect.
    const otherRefreshToken = await otherPage.evaluate(() =>
      window.localStorage.getItem("solidping_refresh_token")
    );
    expect(otherRefreshToken).toBeTruthy();

    const refreshResponse = await page.request.post("/api/v1/auth/refresh", {
      data: { refreshToken: otherRefreshToken },
    });
    expect(refreshResponse.status()).toBe(401);

    await otherContext.close();
  });

  test("the API tokens page never lists a session row", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/account/tokens");
    await page.waitForLoadState("networkidle");

    // A session row's data-testid is unique to the sessions page — assert
    // none of it leaked onto the tokens table.
    await expect(page.locator("[data-testid^='session-row-']")).toHaveCount(0);
  });
});
