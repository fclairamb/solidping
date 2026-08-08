import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * Embeddable status widget, `/embed/v1/widget.js` (spec 2026-08-08-08).
 *
 * These tests deliberately run the widget from a FOREIGN origin: the fixture
 * page is served by a Playwright route on `http://widget-host.test/`, so the
 * script load and the summary fetch are genuinely cross-origin, exactly as
 * they are on a customer's site. Anything that only worked same-origin (a
 * missing CORS header, a credentialed fetch) fails here.
 *
 * Everything under `/embed/v1/` is a frozen public contract, so these are
 * contract tests, not implementation tests: they assert what a customer's page
 * ends up with, not how the widget got there.
 */

const FIXTURE_ORIGIN = "http://widget-host.test";
const FIXTURE_URL = `${FIXTURE_ORIGIN}/embed-fixture.html`;

/** Credentials per seeded org: `make dev` seeds `default`, test mode `test`. */
const ORG_LOGINS = [
  { org: "default", email: "admin@solidping.com", password: "solidpass" },
  { org: "test", email: "test@test.com", password: "test" },
];

const PILL = '[data-testid="solidping-widget-pill"]';
const LABEL = '[data-testid="solidping-widget-label"]';

async function login(
  request: APIRequestContext,
): Promise<{ org: string; token: string }> {
  const wanted = process.env.E2E_ORG;
  const candidates = wanted
    ? ORG_LOGINS.filter((c) => c.org === wanted)
    : ORG_LOGINS;

  for (const { org, email, password } of candidates) {
    const response = await request.post(`${BASE}/api/v1/auth/login`, {
      data: { org, email, password },
    });
    if (response.ok()) {
      const body = (await response.json()) as { accessToken: string };

      return { org, token: body.accessToken };
    }
  }

  throw new Error(`Could not log in to ${BASE} for any known seeded org`);
}

/** Seeds a public, enabled status page the widget can actually resolve. */
async function seedPublicPage(
  request: APIRequestContext,
): Promise<{ org: string; slug: string; name: string }> {
  const { org, token } = await login(request);
  const suffix = Date.now().toString().slice(-9);
  const slug = `e2e-widget-${suffix}`;
  const name = `E2E Widget ${suffix}`;

  const response = await request.post(`${BASE}/api/v1/orgs/${org}/status-pages`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, slug, enabled: true, visibility: "public" },
  });
  expect(
    response.status(),
    `POST status-pages -> ${await response.text()}`,
  ).toBeLessThan(300);

  return { org, slug, name };
}

/**
 * Serves a host page on a foreign origin carrying the exact snippet we hand
 * customers in the dashboard. Only the fixture HTML is intercepted; the
 * widget script and the summary API are fetched from the real server.
 */
async function gotoFixture(page: Page, scriptAttributes: string) {
  const html = `<!doctype html>
<html><head><meta charset="utf-8"><title>host site</title></head>
<body>
  <h1>Customer marketing site</h1>
  <p id="anchor">Status:
    <script async src="${BASE}/embed/v1/widget.js" ${scriptAttributes}></script>
  </p>
</body></html>`;

  await page.route(`${FIXTURE_ORIGIN}/**`, (route) =>
    route.fulfill({ status: 200, contentType: "text/html", body: html }),
  );
  await page.goto(FIXTURE_URL);
}

/** Fulfills every summary request with a caller-supplied hostile payload. */
async function stubSummary(page: Page, body: unknown) {
  await page.route("**/api/v1/status-pages/**/summary", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { "Access-Control-Allow-Origin": "*" },
      body: JSON.stringify(body),
    }),
  );
}

test.describe("Embeddable status widget", () => {
  test.describe.configure({ timeout: 90_000 });

  test("serves the widget script with a long public cache lifetime", async ({
    request,
  }) => {
    const response = await request.get(`${BASE}/embed/v1/widget.js`);
    expect(response.status()).toBe(200);
    expect(response.headers()["content-type"]).toContain(
      "application/javascript",
    );
    expect(response.headers()["cache-control"]).toBe("public, max-age=3600");
  });

  test("renders an inline pill in shadow DOM, linking to the status page", async ({
    page,
    request,
  }) => {
    const { org, slug } = await seedPublicPage(request);

    await gotoFixture(page, `data-page="${org}/${slug}"`);

    // Playwright selectors pierce open shadow roots, so this locator only
    // resolves if the pill really is inside the shadow tree.
    const pill = page.locator(PILL);
    await expect(pill).toBeVisible({ timeout: 30_000 });

    // A freshly created page has no resources, so it rolls up to "unknown"
    // (models.RollupPageStatus) — the label is the built-in English default.
    await expect(page.locator(LABEL)).toHaveText("Status unknown");

    const href = await pill.getAttribute("href");
    expect(href).toContain(`/status0/${org}/${slug}`);
    expect(href).toMatch(/^https?:\/\//);
    expect(await pill.getAttribute("rel")).toContain("noopener");

    // Inline mode renders where the tag sits, not appended to <body>.
    const insideAnchor = await page.evaluate(
      () => document.querySelector("#anchor [data-solidping-widget]") !== null,
    );
    expect(insideAnchor).toBe(true);

    // The host element must be a shadow host, not plain light DOM.
    const hasShadowRoot = await page.evaluate(
      () =>
        (document.querySelector("[data-solidping-widget]") as HTMLElement)
          .shadowRoot !== null,
    );
    expect(hasShadowRoot).toBe(true);
  });

  test("renders a fixed corner pill in floating mode", async ({
    page,
    request,
  }) => {
    const { org, slug } = await seedPublicPage(request);

    await gotoFixture(
      page,
      `data-page="${org}/${slug}" data-mode="floating" data-position="bottom-left" data-theme="dark"`,
    );

    const pill = page.locator(PILL);
    await expect(pill).toBeVisible({ timeout: 30_000 });

    // Floating mode escapes the surrounding paragraph and is appended to
    // <body> so `position: fixed` is not trapped by a transformed ancestor.
    const placement = await page.evaluate(() => {
      const host = document.querySelector(
        "[data-solidping-widget]",
      ) as HTMLElement;
      const root = host.shadowRoot!.querySelector(".sp-root") as HTMLElement;

      return {
        parentIsBody: host.parentElement === document.body,
        position: getComputedStyle(root).position,
        classes: root.className,
      };
    });
    expect(placement.parentIsBody).toBe(true);
    expect(placement.position).toBe("fixed");
    expect(placement.classes).toContain("sp-bottom-left");
    expect(placement.classes).toContain("sp-dark");
  });

  test("applies per-state label overrides", async ({ page, request }) => {
    const { org, slug } = await seedPublicPage(request);

    await gotoFixture(
      page,
      `data-page="${org}/${slug}" data-label-unknown="Checking things"`,
    );

    await expect(page.locator(LABEL)).toHaveText("Checking things", {
      timeout: 30_000,
    });
  });

  test("renders nothing at all for an unknown page slug", async ({ page }) => {
    await gotoFixture(page, `data-page="nope-org/nope-slug"`);

    // Give the widget more than enough time to have rendered something.
    await page.waitForTimeout(3000);

    expect(await page.locator("[data-solidping-widget]").count()).toBe(0);
    expect(await page.locator(PILL).count()).toBe(0);
    // The host page must be untouched — no error state, no stray nodes.
    await expect(page.locator("#anchor")).toHaveText(/Status:/);
  });

  test("renders nothing when the summary request fails outright", async ({
    page,
  }) => {
    await page.route("**/api/v1/status-pages/**/summary", (route) =>
      route.abort(),
    );
    await gotoFixture(page, `data-page="any/thing"`);

    await page.waitForTimeout(3000);
    expect(await page.locator("[data-solidping-widget]").count()).toBe(0);
  });

  // --- Security: this script runs on pages we do not control. ---

  test("renders a hostile label as text, never as markup", async ({ page }) => {
    await stubSummary(page, {
      status: "operational",
      page: { name: "ok", slug: "ok", url: `${BASE}/status0/x/y` },
    });

    const hostile = `<img src=x onerror="window.__pwned=1">`;
    await gotoFixture(
      page,
      `data-page="x/y" data-label-operational="${hostile.replace(/"/g, "&quot;")}"`,
    );

    const label = page.locator(LABEL);
    await expect(label).toBeVisible({ timeout: 30_000 });
    // Verbatim text — the angle brackets survived as characters.
    await expect(label).toHaveText(hostile);

    const injected = await page.evaluate(() => {
      const host = document.querySelector(
        "[data-solidping-widget]",
      ) as HTMLElement;

      return {
        images: host.shadowRoot!.querySelectorAll("img").length,
        pwned: (window as unknown as { __pwned?: number }).__pwned ?? 0,
      };
    });
    expect(injected.images).toBe(0);
    expect(injected.pwned).toBe(0);
  });

  test("drops a hostile page.url instead of turning it into an href", async ({
    page,
  }) => {
    await stubSummary(page, {
      status: "down",
      page: {
        name: "evil",
        slug: "evil",
        url: "javascript:window.__pwned=1",
      },
    });

    await gotoFixture(page, `data-page="x/y"`);

    const pill = page.locator(PILL);
    await expect(pill).toBeVisible({ timeout: 30_000 });
    await expect(page.locator(LABEL)).toHaveText("Major outage");

    const anchor = await page.evaluate(() => {
      const host = document.querySelector(
        "[data-solidping-widget]",
      ) as HTMLElement;
      const el = host.shadowRoot!.querySelector(
        '[data-testid="solidping-widget-pill"]',
      )!;

      return { tag: el.tagName.toLowerCase(), href: el.getAttribute("href") };
    });
    // No anchor, no href: an unusable scheme degrades to a plain, inert pill.
    expect(anchor.href).toBeNull();
    expect(anchor.tag).toBe("span");
  });

  test("keeps the pill inert for a non-http page.url scheme (data:)", async ({
    page,
  }) => {
    await stubSummary(page, {
      status: "operational",
      page: {
        name: "ok",
        slug: "ok",
        url: "data:text/html,<script>window.__pwned=1</script>",
      },
    });

    await gotoFixture(page, `data-page="x/y"`);

    await expect(page.locator(PILL)).toBeVisible({ timeout: 30_000 });
    expect(await page.locator(PILL).getAttribute("href")).toBeNull();
  });

  // Positive control for the two tests above: with a legitimate http(s) URL
  // the very same code path DOES produce a clickable anchor, so those
  // assertions cannot be passing merely because nothing ever links.
  test("still links when page.url is a legitimate https URL", async ({
    page,
  }) => {
    await stubSummary(page, {
      status: "operational",
      page: { name: "ok", slug: "ok", url: "https://status.example.com/" },
    });

    await gotoFixture(page, `data-page="x/y"`);

    const pill = page.locator(PILL);
    await expect(pill).toBeVisible({ timeout: 30_000 });
    expect(await pill.getAttribute("href")).toBe("https://status.example.com/");
    expect(
      await page.evaluate(() => {
        const host = document.querySelector(
          "[data-solidping-widget]",
        ) as HTMLElement;

        return host
          .shadowRoot!.querySelector(
            '[data-testid="solidping-widget-pill"]',
          )!
          .tagName.toLowerCase();
      }),
    ).toBe("a");
  });

  test("never sends credentials with the summary request", async ({ page }) => {
    let sawCookieOrAuth = false;
    await page.route("**/api/v1/status-pages/**/summary", async (route) => {
      const headers = route.request().headers();
      if (headers["cookie"] || headers["authorization"]) {
        sawCookieOrAuth = true;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { "Access-Control-Allow-Origin": "*" },
        body: JSON.stringify({
          status: "operational",
          page: { name: "ok", slug: "ok", url: "https://status.example.com/" },
        }),
      });
    });

    await gotoFixture(page, `data-page="x/y"`);
    await expect(page.locator(PILL)).toBeVisible({ timeout: 30_000 });
    expect(sawCookieOrAuth).toBe(false);
  });
});
