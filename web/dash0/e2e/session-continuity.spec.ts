import type { WebSocket } from "@playwright/test";
import { test, expect, type Page } from "./fixtures";

/** Waits for the next `/events/ws` socket to open and reach the `hello`
 * (authenticated) state. Mirrors live-updates.spec.ts's waitForLiveSubscribed
 * but stops at `hello` rather than `subscribed` — this file cares about
 * re-authentication after a 4401, not the subscribe/hint pipeline. */
async function waitForLiveSocketHello(page: Page): Promise<WebSocket> {
  const ws = await page.waitForEvent("websocket", {
    predicate: (socket) => socket.url().includes("/events/ws"),
    timeout: 15000,
  });
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("timed out waiting for a hello frame")),
      15000
    );
    ws.on("framereceived", (frame) => {
      const text = typeof frame.payload === "string" ? frame.payload : "";
      if (text.includes('"type":"hello"')) {
        clearTimeout(timer);
        resolve();
      }
    });
  });
  return ws;
}

// Requires a server started with a short SP_AUTH_ACCESS_TOKEN_EXPIRY (e.g.
// SP_AUTH_ACCESS_TOKEN_EXPIRY=10s) so the access-token lifetime can actually
// be crossed within a test's timeout — the shared devloop / main CI server
// this suite otherwise runs against is started once by global-setup.ts (or
// ci.yml's "Start solidping server" step) with the default 1h expiry (see
// server/internal/config/config.go), which no test can wait out. Point
// E2E_ACCESS_TOKEN_EXPIRY_SECONDS at whatever value the side-car server was
// actually started with (must match, so the test knows how long to wait) to
// opt in; otherwise every test here is skipped with a clear reason instead
// of silently passing or hanging.
//
// CI runs this file for real: .github/workflows/ci.yml's e2e-tests job
// starts a second, disposable `solidping serve` on :4001 with
// SP_AUTH_ACCESS_TOKEN_EXPIRY=10s (pointed at the same already-seeded
// Postgres DB — CreateTestData is idempotent, so no reset is needed) after
// the main E2E run, then runs just this spec file against it with
// E2E_BASE_URL/E2E_ACCESS_TOKEN_EXPIRY_SECONDS set. To reproduce locally:
//   SP_RUNMODE=test PORT=4001 SP_AUTH_ACCESS_TOKEN_EXPIRY=10s \
//     SP_DB_RESET=true ./solidping serve &
//   E2E_BASE_URL=http://localhost:4001/dash0/ \
//     E2E_ACCESS_TOKEN_EXPIRY_SECONDS=10 CI=true \
//     bunx playwright test session-continuity.spec.ts
// (CI=true skips global-setup's build-and-start-its-own-server step — see
// global-setup.ts — so it doesn't fight the side-car server for :4000/DB.
// PORT is the env config.go reads into server.listen — SOLIDPING_LISTEN,
// mentioned in an earlier version of this comment, is not read anywhere.)
const configuredExpirySeconds = process.env.E2E_ACCESS_TOKEN_EXPIRY_SECONDS
  ? parseInt(process.env.E2E_ACCESS_TOKEN_EXPIRY_SECONDS, 10)
  : undefined;

test.describe("Session continuity past access-token expiry", () => {
  test.beforeEach(() => {
    test.skip(
      !configuredExpirySeconds,
      "requires a side-car server started with a short SP_AUTH_ACCESS_TOKEN_EXPIRY " +
        "and E2E_ACCESS_TOKEN_EXPIRY_SECONDS set to match — see file header"
    );
  });

  test("the reactive 401 path silently refreshes and the next action succeeds", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const expirySeconds = configuredExpirySeconds!;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // Wait past the access token's expiry. The proactive 60s timer in
    // AuthContext won't have fired yet for a short (e.g. 10s) expiry, so
    // whatever refreshes the session here is the reactive 401 path in
    // apiFetch — exactly what this test is meant to isolate.
    await page.waitForTimeout((expirySeconds + 2) * 1000);

    // The next user action (a plain reload, which re-fetches org data
    // through apiFetch) must succeed with no redirect to login.
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page).not.toHaveURL(/session_expired=true/);
    await expect(page).not.toHaveURL(/\/login/);
    // The checks list is real authenticated content — its presence proves
    // the session survived, not just that the page didn't crash.
    await expect(page.getByRole("heading", { name: /checks/i }).first()).toBeVisible();
  });

  // Spec 2026-07-08-01 acceptance criterion (the primary one): "a logged-in
  // dashboard left open across an access-token expiry keeps working — API
  // calls succeed and the live socket re-authenticates with a fresh token
  // after the 4401 — with no re-login." The two tests above prove API calls
  // survive (via a reload after the wait); this one proves the live socket
  // itself recovers on its own, with the tab left open the whole time — no
  // reload, no user interaction.
  test("the live socket reconnects with a fresh token after a 4401 close, with no reload", async ({
    authenticatedPage,
  }) => {
    test.setTimeout(120_000);

    const page = authenticatedPage;
    const expirySeconds = configuredExpirySeconds!;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // The first socket authenticates with the token minted at login.
    await waitForLiveSocketHello(page);

    // Wait past expiry with NO reload and no interaction. The server closes
    // the now-expired socket with 4401; live-socket.ts's onclose handler
    // refreshes before reconnecting (A.2/A.3), and the next attempt should
    // reach `hello` again — proving the *socket itself* recovered, not just
    // that a later reload picked up a fresh token.
    const secondHello = waitForLiveSocketHello(page);
    await Promise.race([
      secondHello,
      page.waitForTimeout((expirySeconds + 30) * 1000).then(() => {
        throw new Error("no second authenticated socket within the expected window");
      }),
    ]);

    // The tab never left the checks page — no redirect, no reload.
    await expect(page).not.toHaveURL(/\/login/);
    await expect(page).toHaveURL(/\/checks/);

    // A plain authenticated API call also still succeeds, proving the
    // refreshed token is usable outside the socket too.
    const token = await page.evaluate(() =>
      window.localStorage.getItem("solidping_session_token")
    );
    const resp = await page.request.get("/api/v1/orgs/test/checks?limit=1", {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(resp.status()).toBe(200);
  });

  test("the proactive timer refreshes before the user does anything", async ({
    authenticatedPage,
  }) => {
    // The (expirySeconds + 65)s wait below comfortably exceeds Playwright's
    // default 30s per-test timeout — extend it, or this always times out
    // rather than actually exercising the proactive timer (discovered
    // running this suite for real against a side-car server per the file
    // header; CI's normal run skips this whole file, so nothing else catches it).
    test.setTimeout(120_000);

    const page = authenticatedPage;
    const expirySeconds = configuredExpirySeconds!;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // Wait past expiry with NO intervening navigation or API call — if
    // anything kept the session alive here, it was the 60s proactive
    // interval in AuthContext, not a reactive 401 (there was no request to
    // 401 on). A very short expiry (well under 60s) means the timer's
    // first tick already landed inside the token's lifetime — this
    // specifically exercises "isn't yet in the last third" not firing
    // early, then does fire once inside the window.
    await page.waitForTimeout((expirySeconds + 65) * 1000);

    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page).not.toHaveURL(/session_expired=true/);
    await expect(page).not.toHaveURL(/\/login/);
  });

  // Spec 2026-07-08-01 acceptance criterion: "a localStorage session with an
  // access token but no refresh token redirects to login promptly (no
  // unbounded reconnect churn; the socket makes at most ~2 attempts with a
  // dead token)". This is the funnel-audit failure class the spec's captured
  // HAR diagnosed (setSession skipped, or in this test's case, a refresh
  // token that's simply gone) — token-refresh.ts's escalate() (A.2) and the
  // proactive timer (AuthContext) or live-socket's pre-dial check (A.3)
  // should catch it without any user interaction or unbounded retry.
  test("an access-token-only session (no refresh token) auto-redirects to login without unbounded socket churn", async ({
    authenticatedPage,
  }) => {
    // See the timeout comment on "the proactive timer..." above — this test
    // waits past the same (expirySeconds + 65)s margin.
    test.setTimeout(120_000);

    const page = authenticatedPage;
    const expirySeconds = configuredExpirySeconds!;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // Simulate the funnel-audit failure class directly: an access token
    // with no refresh token, rather than reproducing a specific broken
    // login path (those are covered by lib/oauth-handoff.test.ts and
    // token-refresh.test.ts).
    await page.evaluate(() => {
      window.localStorage.removeItem("solidping_refresh_token");
    });

    let wsAttempts = 0;
    page.on("websocket", (ws) => {
      if (ws.url().includes("/events/ws")) wsAttempts += 1;
    });

    // Wait past expiry (same margin as the proactive-timer test above) with
    // NO user interaction: the proactive 60s timer in AuthContext should
    // find there's no refresh token to use and escalate — clear the
    // session and redirect — on its own, before any reactive 401 or manual
    // action.
    await page.waitForTimeout((expirySeconds + 65) * 1000);

    await page.waitForURL(/\/login/, { timeout: 10000 });
    await expect(page).toHaveURL(/session_expired=true/);

    // The live socket must not have hammered the server with the dead
    // token for the whole ~75s+ wait — a handful of attempts (the pre-dial
    // check plus at most one or two reconnects before give-up) is fine;
    // dozens (the captured incident's 139-in-70-minutes rate) is not.
    expect(wsAttempts).toBeLessThanOrEqual(5);
  });
});
