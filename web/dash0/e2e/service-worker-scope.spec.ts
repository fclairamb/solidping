import { test, expect } from "@playwright/test";
import { API_BASE, DASH_BASE } from "./fixtures";

/**
 * The service-worker scope move (spec 2026-09-09-01 §5).
 *
 * A push subscription is bound to a service-worker REGISTRATION, and a
 * registration is keyed by its scope. The dashboard's worker used to be
 * registered from `/dash0/sw.js` (scope `/dash0/`); it is now registered from
 * `${BASE_URL}sw.js`. A page under `/d/` is not inside scope `/dash0/`, so
 * `navigator.serviceWorker.ready` — what `useWebPushSubscription` waits on —
 * would never resolve against the old registration, and the old worker would
 * sit there forever holding a dead subscription.
 *
 * The `301` on `/dash0/*` does NOT clean this up: a browser refuses a
 * service-worker script that answers with a redirect, so the old worker is
 * silently kept. It has to be unregistered explicitly, which is what
 * `lib/service-worker.ts` does on boot.
 */
test.describe("service worker scope", () => {
  test("the legacy sw.js URL is a redirect, which cannot update a worker", async ({
    request,
  }) => {
    const response = await request.get(`${API_BASE}/dash0/sw.js`, {
      maxRedirects: 0,
    });

    // A redirected script is rejected by the browser's service-worker update
    // algorithm — this is precisely why the stale registration survives and
    // has to be torn down by hand.
    expect(response.status()).toBe(301);
    expect(response.headers()["location"]).toBe(`${DASH_BASE}/sw.js`);
  });

  test("a registration left on the legacy scope is removed on boot", async ({
    page,
    context,
  }) => {
    // The legacy script URL now redirects, so it cannot be registered for
    // real. Serve a stand-in body at that URL to recreate exactly the state a
    // returning user's browser is in.
    await context.route("**/dash0/sw.js", (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/javascript",
        body: "self.addEventListener('install', () => self.skipWaiting());\n",
      }),
    );

    await page.goto(`${DASH_BASE}/login`);

    // Seed the stale registration alongside whatever the app registered.
    await page.evaluate(async () => {
      await navigator.serviceWorker.register("/dash0/sw.js", {
        scope: "/dash0/",
      });
    });

    const scopes = async () =>
      page.evaluate(async () =>
        (await navigator.serviceWorker.getRegistrations()).map(
          (r) => new URL(r.scope).pathname,
        ),
      );

    await expect.poll(scopes, { timeout: 15000 }).toContain("/dash0/");

    // Boot the app again: registerServiceWorker() runs on every load.
    await page.goto(`${DASH_BASE}/login`);

    await expect
      .poll(scopes, { timeout: 15000 })
      .toEqual([`${DASH_BASE}/`]);
  });
});
