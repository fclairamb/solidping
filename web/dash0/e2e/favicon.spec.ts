import { test, expect, type Page } from "@playwright/test";
import { API_BASE, DASH_BASE, STATUS_BASE, escapeRegExp } from "./fixtures";

/**
 * Favicon regression guard (spec: favicons under public/assets/).
 *
 * The failure mode this protects against: an icon href in index.html that is
 * document-relative (href="favicon.svg") resolves against the current SPA
 * route on deep links (/d/orgs/x/checks/favicon.svg). The server answers
 * unknown paths with the index.html SPA fallback — HTTP 200, text/html — so
 * the browser silently renders a broken favicon instead of 404ing.
 *
 * The guard therefore loads a DEEP route in each app and asserts that every
 * <head> icon/manifest link, resolved exactly like a browser would resolve
 * it (against the document URL), returns real image/manifest bytes.
 */

/** Resolve every icon-ish <link> href against the document URL and fetch it. */
async function collectHeadLinks(page: Page) {
  const links = await page
    .locator('link[rel~="icon"], link[rel="apple-touch-icon"], link[rel="manifest"]')
    .evaluateAll((elements) =>
      elements.map((el) => ({
        rel: el.getAttribute("rel") ?? "",
        // el.href is the browser-resolved absolute URL (vs getAttribute's raw value)
        url: (el as HTMLLinkElement).href,
      })),
    );
  expect(links.length).toBeGreaterThanOrEqual(3);
  return links;
}

async function assertLinksServeRealAssets(page: Page, appBase: string) {
  const links = await collectHeadLinks(page);

  for (const link of links) {
    // Base-absolute: the href must resolve under the app base, not under the
    // current route's directory.
    expect(new URL(link.url).pathname, `${link.rel} href must be app-base absolute`).toMatch(
      new RegExp(`^${appBase}/`),
    );

    const response = await page.request.get(link.url);
    expect(response.status(), `${link.url} must be served`).toBe(200);

    const contentType = response.headers()["content-type"] ?? "";
    expect(contentType, `${link.url} must not fall back to index.html`).not.toContain("text/html");

    if (link.rel === "manifest") {
      // The manifest must parse, and its icons (manifest-relative srcs) must
      // themselves resolve to images.
      const manifest = JSON.parse((await response.body()).toString());
      expect(manifest.icons.length).toBeGreaterThanOrEqual(2);
      for (const icon of manifest.icons) {
        const iconURL = new URL(icon.src, link.url).toString();
        const iconResponse = await page.request.get(iconURL);
        expect(iconResponse.status(), `${iconURL} must be served`).toBe(200);
        expect(
          iconResponse.headers()["content-type"] ?? "",
          `${iconURL} must be an image`,
        ).toContain("image/");
      }
    } else {
      expect(contentType, `${link.url} must be an image`).toContain("image/");
    }
  }
}

test.describe("favicons resolve on deep SPA routes", () => {
  test(`dash0: icons load from ${DASH_BASE}/assets/ on a nested route`, async ({ page }) => {
    // The login page is a deep route (3 path segments) that needs no auth.
    await page.goto("orgs/test/login");
    await assertLinksServeRealAssets(page, `${DASH_BASE}`);
  });

  test(`status0: icons load from ${STATUS_BASE}/assets/ on a nested route`, async ({ page }) => {
    // Any nested path serves the SPA index (page existence is irrelevant to
    // the <head> links, which are static).
    await page.goto(`${API_BASE}${STATUS_BASE}/test/some-page`);
    await assertLinksServeRealAssets(page, `${STATUS_BASE}`);
  });

  test("dash0 service worker's notification icon resolves to a served asset", async ({
    page,
  }) => {
    // sw.js lives in public/, so Vite copies it verbatim and it can carry no
    // build-time `base`. Since spec 2026-09-09-01 (/dash0 -> /d) every path in
    // it is derived at RUNTIME from `self.registration.scope` through the
    // `appPath()` helper — which is precisely what let the worker survive the
    // move. There is therefore no absolute icon literal left to grep for, and
    // asserting one again would re-introduce the thing that broke.
    //
    // What is still worth protecting is the property the literal used to
    // stand for: the icon the worker computes is a real, served asset. So
    // extract the argument the worker hands to appPath(), check it is
    // scope-relative (the base-agnostic bit), resolve it exactly as appPath
    // would against the worker's real scope, and fetch it.
    const response = await page.request.get(`${API_BASE}${DASH_BASE}/sw.js`);
    expect(response.status()).toBe(200);
    const source = (await response.body()).toString();

    const iconMatch = source.match(/icon:\s*appPath\((['"`])([^'"`]+)\1\)/);
    expect(
      iconMatch,
      "sw.js must still compute its notification icon via appPath(<relative path>)",
    ).not.toBeNull();

    const iconRelative = iconMatch![2];
    expect(
      iconRelative.startsWith("/"),
      `sw.js icon "${iconRelative}" must be scope-relative, not an absolute literal`,
    ).toBe(false);

    // appPath() === new URL(relative, self.registration.scope); the worker's
    // real scope is the app base, which always ends in a slash.
    const iconURL = new URL(iconRelative, `${API_BASE}${DASH_BASE}/`);
    expect(iconURL.pathname, "the computed icon must live under the app base").toMatch(
      new RegExp(`^${escapeRegExp(DASH_BASE)}/`),
    );

    const iconResponse = await page.request.get(iconURL.toString());
    expect(iconResponse.status(), `${iconURL.href} must be served`).toBe(200);
    expect(
      iconResponse.headers()["content-type"] ?? "",
      `${iconURL.href} must be an image, not the SPA index fallback`,
    ).toContain("image/");
  });
});
