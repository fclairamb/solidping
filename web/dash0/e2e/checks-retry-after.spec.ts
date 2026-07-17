import { test, expect } from "./fixtures";

// Regression guard for spec 2026-07-17-02 (secondary): a 429 must be retried,
// and the retry must honor the server's `Retry-After` header rather than the
// default exponential backoff.
//
// Before the fix, main.tsx's retry predicate bailed on every 4xx (status < 500
// → return false), so a 429 failed the query outright and the next live
// invalidation re-fired it ~3s later — a self-sustaining retry pulse while the
// bucket was empty. Now 429 is retried and `retryDelay` returns
// `Retry-After × 1000` (capped at 60s).
//
// The live WebSocket is stubbed to never connect so the only thing that can
// re-request `/checks` is the retry itself — isolating the timing assertion
// from an incidental live-ack refetch.

test.describe("Checks 429 Retry-After handling", () => {
  test("a 429 with Retry-After is retried once after the honored delay, with no storm before it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Silence the live socket: accept the client connection but never bridge to
    // the server, so no `subscribed` ack (and thus no ack-driven refetch) fires.
    await page.routeWebSocket(/\/events\/ws/, () => {
      /* intentionally never connectToServer() */
    });

    const hitTimestamps: number[] = [];
    let served429 = false;

    // Intercept only the checks page's batched query — it is the sole /checks
    // request carrying limit=100. The command palette also hits /checks (with
    // limit=10) from the org layout, so matching on limit=100 keeps the retry
    // unambiguous. The regex also excludes /checks/{uid}, /checks/export and
    // /check-groups (it requires /checks to be followed by `?` or end-of-URL).
    await page.route(/\/api\/v1\/orgs\/[^/]+\/checks(\?|$)/, async (route) => {
      const req = route.request();
      const url = new URL(req.url());
      if (req.method() !== "GET" || url.searchParams.get("limit") !== "100") {
        await route.continue();
        return;
      }
      hitTimestamps.push(Date.now());
      if (!served429) {
        served429 = true;
        await route.fulfill({
          status: 429,
          headers: {
            "Retry-After": "2",
            "Content-Type": "application/json",
          },
          body: JSON.stringify({
            title: "Too many requests",
            code: "RATE_LIMITED",
          }),
        });
        return;
      }
      // The retry: let the real backend answer so the page recovers.
      await route.continue();
    });

    await page.goto("orgs/test/checks");

    // The retry must land — but not before the honored ~2s delay.
    await expect
      .poll(() => hitTimestamps.length, { timeout: 8000 })
      .toBeGreaterThanOrEqual(2);

    // The gap between the 429 and its retry honors Retry-After (~2s), which is
    // distinguishable from the ~1s (attempt 0) exponential backoff.
    const gapMs = hitTimestamps[1] - hitTimestamps[0];
    expect(
      gapMs,
      `retry landed ${gapMs}ms after the 429 — must honor Retry-After (~2s), not the ~1s backoff`,
    ).toBeGreaterThanOrEqual(1800);

    // No retry storm fired inside the honored window (the old blind-retry pulse
    // would have produced extra hits well before 2s).
    const withinWindow = hitTimestamps.filter(
      (t) => t > hitTimestamps[0] && t < hitTimestamps[0] + 1800,
    );
    expect(
      withinWindow.length,
      "no retry may fire before the honored Retry-After delay elapses",
    ).toBe(0);
  });
});
