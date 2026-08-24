import { test, expect, freshLogin } from "./fixtures";

// Every test here gets its own dedicated login instead of the shared
// `authenticatedPage`/`authWorkerStorageState` fixture. That shared session
// is minted once at worker start and never refreshed, so
// `enforceSessionCap` (server/internal/handlers/auth/service.go) — which
// prunes a user's `refresh` sessions down to 10 on every fresh login
// elsewhere in the suite (login.spec.ts, mcp-oauth-consent.spec.ts,
// membership-requests.spec.ts, this file's own "other session" contexts,
// etc.) — eventually evicts it as the least-recently-active row well before
// this file runs (60+ fresh logins land before this file starts in a full
// suite run). This file specifically asserts on the session's own
// `user_tokens` row surviving (isCurrent flag, an explicit refresh), so it
// cannot ride the shared session. See spec 2026-08-05-02.
test.describe("Sessions", () => {
  test("lists the current session with the current-session badge", async ({
    page,
  }) => {
    await freshLogin(page);

    await page.goto("orgs/test/account/sessions");
    await page.waitForLoadState("networkidle");

    // The login above is this page's own session — it must show up, and be
    // flagged current.
    const currentBadge = page.getByTestId("session-current-badge");
    await expect(currentBadge).toBeVisible();

    const currentRow = currentBadge.locator(
      "xpath=ancestor::*[starts-with(@data-testid, 'session-row-')]"
    );
    await expect(currentRow).toBeVisible();
    // Exactly one row carries the badge.
    await expect(page.getByTestId("session-current-badge")).toHaveCount(1);
  });

  // Regression test for spec 2026-08-24-02: a session minted with a known
  // User-Agent must render the parsed device label ("Chromium on ..."), not
  // the "Unknown device" fallback. This exercises a real password login
  // through an actual browser (Playwright sends its genuine UA header), so
  // it pins the read path (parseUserAgent + the badge) even though a true
  // federated/OAuth login can't be driven end-to-end here without a mock
  // IdP — the backend fix itself (properties.created_with capturing the
  // request-parked UA/IP on every session-minting path, federated included)
  // is covered by the Go table test in
  // server/internal/handlers/auth/created_with_test.go.
  test("a session minted with a known user agent shows its parsed device, not Unknown device", async ({
    page,
  }) => {
    await freshLogin(page);

    await page.goto("orgs/test/account/sessions");
    await page.waitForLoadState("networkidle");

    const currentBadge = page.getByTestId("session-current-badge");
    await expect(currentBadge).toBeVisible();

    const currentRow = currentBadge.locator(
      "xpath=ancestor::*[starts-with(@data-testid, 'session-row-')]"
    );
    const deviceLabel = currentRow.locator("[data-testid^='session-device-label-']");
    await expect(deviceLabel).toBeVisible();
    await expect(deviceLabel).not.toHaveText("Unknown device");
    await expect(deviceLabel).not.toHaveText("");
  });

  test("sign out other sessions deletes every session but the caller's own", async ({
    page,
    browser,
  }) => {
    await freshLogin(page);

    // Open a second, independent session (its own browser context, separate
    // cookie/localStorage jar) so there's an "other" session to sign out.
    const otherContext = await browser.newContext();
    const otherPage = await otherContext.newPage();
    await freshLogin(otherPage);

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
    page,
    browser,
  }) => {
    await freshLogin(page);

    const otherContext = await browser.newContext();
    const otherPage = await otherContext.newPage();
    await freshLogin(otherPage);

    // Identify the session `otherPage` just created by uid (its own
    // "current" row), rather than picking "the first non-current row" from
    // the caller's list — that row is whichever stale session happens to
    // sort first, which is not deterministic once sessions accumulate
    // across a run (see spec 2026-08-05-02).
    const otherSessionsResponse = await otherPage.request.get(
      "/api/v1/orgs/test/tokens?type=refresh"
    );
    expect(otherSessionsResponse.status()).toBe(200);
    const otherSessions = (await otherSessionsResponse.json()).data as Array<{
      uid: string;
      isCurrent?: boolean;
    }>;
    const otherSessionUid = otherSessions.find((s) => s.isCurrent)?.uid;
    expect(otherSessionUid).toBeTruthy();

    await page.goto("orgs/test/account/sessions");
    await page.waitForLoadState("networkidle");

    const rowCountBefore = await page
      .locator("[data-testid^='session-row-']")
      .count();
    expect(rowCountBefore).toBeGreaterThanOrEqual(2);

    const otherRow = page.getByTestId(`session-row-${otherSessionUid}`);
    await expect(otherRow).toBeVisible();
    await otherRow.locator("[data-testid^='session-revoke-button-']").click();
    await page.getByTestId("session-revoke-confirm").click();
    await page.waitForLoadState("networkidle");

    await expect(
      page.locator("[data-testid^='session-row-']")
    ).toHaveCount(rowCountBefore - 1);
    await expect(
      page.getByTestId(`session-row-${otherSessionUid}`)
    ).toHaveCount(0);

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

  test("the API tokens page never lists a session row", async ({ page }) => {
    await freshLogin(page);

    await page.goto("orgs/test/account/tokens");
    await page.waitForLoadState("networkidle");

    // A session row's data-testid is unique to the sessions page — assert
    // none of it leaked onto the tokens table.
    await expect(page.locator("[data-testid^='session-row-']")).toHaveCount(0);
  });
});
