import { test, expect, type Page } from "@playwright/test";
import { API_BASE } from "./fixtures";

/**
 * Favicon regression guard (spec: favicons under public/assets/).
 *
 * The failure mode this protects against: an icon href in index.html that is
 * document-relative (href="favicon.svg") resolves against the current SPA
 * route on deep links (/dash0/orgs/x/checks/favicon.svg). The server answers
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
  test("dash0: icons load from /dash0/assets/ on a nested route", async ({ page }) => {
    // The login page is a deep route (3 path segments) that needs no auth.
    await page.goto("orgs/test/login");
    await assertLinksServeRealAssets(page, "/dash0");
  });

  test("status0: icons load from /status0/assets/ on a nested route", async ({ page }) => {
    // Any nested path serves the SPA index (page existence is irrelevant to
    // the <head> links, which are static).
    await page.goto(`${API_BASE}/status0/test/some-page`);
    await assertLinksServeRealAssets(page, "/status0");
  });

  test("dash0 service worker pushes the assets/ notification icon", async ({ page }) => {
    // sw.js is static (not Vite-processed), so its icon path is hardcoded and
    // easy to break when assets move.
    const response = await page.request.get(`${API_BASE}/dash0/sw.js`);
    expect(response.status()).toBe(200);
    expect((await response.body()).toString()).toContain("/dash0/assets/favicon-192.png");
  });
});
