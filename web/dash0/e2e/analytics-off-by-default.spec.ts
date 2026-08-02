import { test, expect, type Page, type Request } from "@playwright/test";

/**
 * Proves the privacy guarantee of spec 2026-08-02-08: with no PostHog
 * credentials configured (the state of every stock/self-hosted install, which
 * is exactly how the E2E server runs), the dashboard must:
 *
 *  1. download NO analytics JavaScript, and
 *  2. issue ZERO requests to any PostHog host.
 *
 * These tests use the plain `page` fixture (not the authenticated one) so the
 * pre-login boot path — the one that reads the public config endpoint — is
 * covered too.
 */

// Any host that would indicate PostHog ingestion or asset delivery.
const POSTHOG_HOST_RE = /posthog|ph-cdn/i;

// The lazily imported analytics chunk. src/lib/posthog-loader.ts exists purely
// so this chunk gets a recognizable filename instead of an opaque one.
const POSTHOG_CHUNK_RE = /posthog-loader/i;

function collectRequests(page: Page): Request[] {
  const seen: Request[] = [];
  page.on("request", (request) => seen.push(request));

  return seen;
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

test.describe("product analytics is inert when not configured", () => {
  test("the public config endpoint reports disabled and omits the key", async ({ request }) => {
    const response = await request.get("/api/v1/config");
    expect(response.ok()).toBe(true);

    const body = await response.json();
    expect(body).toHaveProperty("posthog");
    expect(body.posthog.enabled).toBe(false);

    // The core contract: ABSENT, not empty-string.
    expect(body.posthog).not.toHaveProperty("projectApiKey");
    expect(body.posthog).not.toHaveProperty("host");

    // And no secret ever leaks through this public endpoint.
    expect(JSON.stringify(body)).not.toContain("personal");
  });

  test("the login page makes zero requests to a PostHog host", async ({ page }) => {
    const requests = collectRequests(page);

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    // The analytics boot runs in an effect after first paint; give it room to
    // misbehave before asserting it did nothing.
    await page.waitForTimeout(2000);

    const urls = requests.map((r) => r.url());

    expect(
      urls.filter((u) => POSTHOG_HOST_RE.test(hostOf(u))),
      "no request may reach a PostHog host when analytics is unconfigured",
    ).toEqual([]);

    expect(
      urls.filter((u) => POSTHOG_CHUNK_RE.test(u)),
      "the posthog-js chunk must never be downloaded when analytics is unconfigured",
    ).toEqual([]);

    // Sanity checks so an empty request list can never be a false pass caused
    // by a page that simply never loaded.
    await expect(page.getByTestId("login-title")).toBeVisible();
    expect(urls.length).toBeGreaterThan(3);
    expect(urls.some((u) => u.includes("/api/v1/config"))).toBe(true);
  });

  test("index.html contains no analytics script tag", async ({ request }) => {
    const response = await request.get("/dash0/");
    const html = await response.text();
    expect(html.toLowerCase()).not.toContain("posthog");
  });
});

/**
 * Positive control for the tests above. Without it, "zero requests" would be
 * indistinguishable from an analytics boot that is simply broken and could
 * never fire under any configuration.
 *
 * The public config endpoint is stubbed to report PostHog as enabled and every
 * PostHog host is intercepted, so the control never leaves the machine.
 */
test.describe("product analytics loads once configured", () => {
  test("stubbing the config endpoint makes the dashboard load posthog-js", async ({ page }) => {
    await page.route("**/api/v1/config", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          posthog: {
            enabled: true,
            projectApiKey: "phc_e2e_positive_control",
            host: "https://eu.i.posthog.com",
          },
        }),
      });
    });

    // Intercept every PostHog call so the positive control stays offline.
    const ingestUrls: string[] = [];
    await page.route(/posthog\.com/, async (route) => {
      ingestUrls.push(route.request().url());
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ status: 1 }),
      });
    });

    const requests = collectRequests(page);

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    // The analytics chunk is fetched lazily as soon as the config resolves.
    await expect
      .poll(() => requests.map((r) => r.url()).filter((u) => POSTHOG_CHUNK_RE.test(u)).length, {
        message: "the posthog-js chunk must be downloaded once the server reports it enabled",
        timeout: 15000,
      })
      .toBeGreaterThan(0);

    // …and it must then actually talk to the configured PostHog host.
    await expect
      .poll(() => ingestUrls.length, {
        message: "an initialized PostHog client must reach its configured host",
        timeout: 20000,
      })
      .toBeGreaterThan(0);
  });
});
