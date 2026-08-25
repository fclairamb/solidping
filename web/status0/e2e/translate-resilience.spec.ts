import {
  test,
  expect,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";

/**
 * Regression guard for the recurring
 *   "Failed to execute 'removeChild' on 'Node'"
 * crash on the public status page (surfaced to visitors as TanStack Router's
 * default "Something went wrong!" screen).
 *
 * Failure class: a machine translator re-parents every text node into nested
 * <font style="vertical-align: inherit"> wrappers, *keeping the original text
 * node object*. React's fiber tree still records the ORIGINAL parent for that
 * text node, so the next commit that removes it calls
 * `originalParent.removeChild(textNode)` and the DOM throws NotFoundError,
 * because the node now lives under a <font>.
 *
 * What these tests actually guard, and what they do not:
 *
 *   * PRODUCTION hardening (has teeth in the CI run against the built bundle):
 *     the elements whose text changes between renders are marked
 *     `translate="no"` (NO_TRANSLATE in components/shared/status-page-view.tsx).
 *     The first test seeds its own status page so that every one of those
 *     elements actually renders, then checks each BY SELECTOR — present, still
 *     opted out, still unwrapped — plus the two hover-only tooltips. Delete any
 *     one of those `translate="no"` attributes and this spec fails.
 *     Caveat: "the simulation honours element-level opt-outs" is the
 *     SIMULATION'S policy. It matches what the HTML spec says `translate="no"`
 *     means, but real Chrome was not driven here, so this proves the markers
 *     are on the DOM — not that Chrome obeys them.
 *   * DEV-ONLY: `#root` must hold exactly one child. A second `createRoot()`
 *     mounted a second app and killed the first with this very error. Only
 *     `vite dev` can cause that (HMR re-executes main.tsx), so that assertion
 *     has teeth against `make dev` and is trivially true against the bundle.
 *   * NOT established: no crash has been reproduced against a production build.
 *     Wrapping literally every text node (the `forced` mode used by the
 *     hostile-DOM test) and driving language switches plus a background refetch
 *     does not throw today. Treat the no-pageerror assertions as a guard for a
 *     documented failure class, not as proof a live bug was fixed.
 */

import { API_BASE as BASE } from "./fixtures";

/** TanStack Router's default error boundary copy. */
const ERROR_BOUNDARY_TEXT = "Something went wrong!";

/**
 * Credentials per seeded org: `make dev` seeds `default`, `SP_RUNMODE=test`
 * (CI) seeds `test` instead, so probe rather than hardcode.
 */
const ORG_LOGINS = [
  { org: "default", email: "admin@solidping.io", password: "solidpass" },
  { org: "test", email: "test@test.com", password: "test" },
];

/** Finds a public status page to exercise (no seeding, no auth). */
async function resolveStatusPageUrl(request: APIRequestContext): Promise<string> {
  const orgs = process.env.E2E_ORG ? [process.env.E2E_ORG] : ["default", "test"];
  for (const org of orgs) {
    const response = await request.get(`${BASE}/api/v1/status-pages/${org}`);
    if (response.ok()) return `${BASE}/status0/${org}`;
  }
  throw new Error(
    `No public status page found on ${BASE} for orgs ${orgs.join(", ")}`,
  );
}

type Json = Record<string, unknown>;

/** Names the check this spec parks inside a maintenance window. */
const MAINT_CHECK_PREFIX = "e2e-translate-maint-";

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

/**
 * Seeds a status page that actually renders EVERY translate-hardened element.
 *
 * The seeded fixture the servers ship with has no sections and no resources, so
 * a test pointed at it would only ever see three of the hardened elements and
 * the hardened-count assertion would pass for the wrong reason. This builds:
 *
 *   * a resource on a pre-existing check (it already has results, so the
 *     availability bar and percentage render),
 *   * a second resource whose check sits inside an ACTIVE maintenance window
 *     (so the maintenance badge renders alongside the normal status badge), and
 *   * a published status update (so the incident-kind badge and the relative
 *     <time> render).
 */
async function seedRichStatusPage(
  request: APIRequestContext,
): Promise<{ url: string; org: string }> {
  const { org, token } = await login(request);
  const auth = { Authorization: `Bearer ${token}` };
  const suffix = Date.now().toString().slice(-9);

  const post = async (path: string, data: Json): Promise<Json> => {
    const response = await request.post(`${BASE}${path}`, {
      headers: auth,
      data,
    });
    expect(
      response.status(),
      `POST ${path} -> ${await response.text()}`,
    ).toBeLessThan(300);
    return (await response.json()) as Json;
  };

  // A pre-seeded check has results behind it, which is what makes the
  // availability bar and the "%" span render at all. It must NOT be one of the
  // maintenance checks a previous run of this spec left behind, otherwise both
  // resources would render the maintenance badge and the normal status badge
  // would never appear.
  const checksResponse = await request.get(
    `${BASE}/api/v1/orgs/${org}/checks?limit=100`,
    { headers: auth },
  );
  const checksBody = (await checksResponse.json()) as {
    data?: (Json & { name?: string; status?: string; inMaintenance?: boolean })[];
  };
  const usable = (checksBody.data ?? []).filter(
    (check) =>
      !check.inMaintenance && !String(check.name).startsWith(MAINT_CHECK_PREFIX),
  );
  const existingCheck =
    usable.find((check) => Boolean(check.status) && check.status !== "unknown") ??
    usable[0];
  expect(
    existingCheck,
    "no seeded, non-maintenance check to attach a resource to",
  ).toBeTruthy();

  const maintenanceCheck = await post(`/api/v1/orgs/${org}/checks`, {
    type: "http",
    name: `${MAINT_CHECK_PREFIX}${suffix}`,
    config: { url: "https://example.com" },
    period: "00:05:00",
  });

  const slug = `e2e-translate-${suffix}`.slice(0, 40);
  const statusPage = await post(`/api/v1/orgs/${org}/status-pages`, {
    name: `E2E Translate ${suffix}`,
    slug,
    visibility: "public",
  });
  const section = await post(
    `/api/v1/orgs/${org}/status-pages/${statusPage.uid}/sections`,
    { name: "Core", slug: `core-${suffix}`.slice(0, 40) },
  );

  const resourcePath = `/api/v1/orgs/${org}/status-pages/${statusPage.uid}/sections/${section.uid}/resources`;
  await post(resourcePath, { checkUid: existingCheck!.uid, position: 0 });
  await post(resourcePath, { checkUid: maintenanceCheck.uid, position: 1 });

  // Active window over the second check -> resource.check.inMaintenance.
  const now = Date.now();
  const window = await post(`/api/v1/orgs/${org}/maintenance-windows`, {
    title: `E2E Translate window ${suffix}`,
    startAt: new Date(now - 5 * 60_000).toISOString(),
    endAt: new Date(now + 60 * 60_000).toISOString(),
    recurrence: "none",
  });
  const setChecks = await request.put(
    `${BASE}/api/v1/orgs/${org}/maintenance-windows/${window.uid}/checks`,
    { headers: auth, data: { checkUids: [maintenanceCheck.uid] } },
  );
  expect(setChecks.status(), await setChecks.text()).toBeLessThan(300);

  await post(`/api/v1/orgs/${org}/status-updates`, {
    statusPageUid: statusPage.uid,
    kind: "investigating",
    title: `E2E translate update ${suffix}`,
    bodyMarkdown: "Looking into it.",
    publishedAt: new Date(now - 5 * 60_000).toISOString(),
  });

  return { url: `${BASE}/status0/${org}/${slug}`, org };
}

/**
 * Installs a simulation of a machine translator (Chrome auto-translate, the
 * Google Translate widget, a translating extension):
 *
 *  - every visible HTML text node is MOVED into nested <font> wrappers (the
 *    same node object, exactly like Chrome — this is what breaks React), and
 *  - a MutationObserver keeps doing it to anything rendered later, which is how
 *    a translator keeps a live SPA translated.
 *
 * Two modes:
 *
 *   "respectOptOut" (default) — models the realistic worst case: a visitor who
 *     explicitly picked "Translate to…", so the DOCUMENT-level hints in
 *     index.html (`<html translate="no">`, the notranslate meta) are overridden,
 *     but ELEMENT-level `translate="no"` / `.notranslate` subtrees are still
 *     honoured, which is what real translators do. This is the mode that gives
 *     the hardening in status-page-view.tsx something to prove.
 *
 *   "forced" — ignores every opt-out and wraps everything. Not a real
 *     translator; it is the maximally hostile DOM used to check the app still
 *     survives even if some future actor ignores the hints entirely.
 *
 * SVG text is skipped in both modes: translators leave it alone, and wrapping
 * it would manufacture a failure mode that cannot happen in the field.
 */
type TranslateMode = "respectOptOut" | "forced";

async function simulateChromeTranslate(
  page: Page,
  mode: TranslateMode = "respectOptOut",
): Promise<number> {
  return page.evaluate((mode: TranslateMode) => {
    interface TranslateWindow extends Window {
      __spTranslateWrapped?: number;
      __spTranslateObserver?: MutationObserver;
    }
    const w = window as TranslateWindow;
    if (w.__spTranslateObserver) return w.__spTranslateWrapped ?? 0;
    w.__spTranslateWrapped = 0;

    const HTML_NS = "http://www.w3.org/1999/xhtml";

    const shouldWrap = (node: Node): node is Text => {
      if (node.nodeType !== Node.TEXT_NODE) return false;
      if (!node.nodeValue || !node.nodeValue.trim()) return false;
      const parent = (node as Text).parentElement;
      if (!parent) return false;
      // Skip SVG (recharts) — Chrome leaves it alone.
      if (parent.namespaceURI !== HTML_NS) return false;
      // Never touch the operator stylesheet, scripts, form values, or text we
      // already wrapped (that would loop forever).
      if (parent.closest("script, style, textarea, font")) return false;
      if (mode === "respectOptOut") {
        // Element-level opt-out, honoured even when the visitor forces a
        // translation. `<html translate="no">` is deliberately NOT treated as a
        // boundary here — that is the document-level hint the visitor just
        // overrode, and treating it as one would make this simulation a no-op.
        const optOut = parent.closest('[translate="no"], .notranslate');
        if (optOut && optOut !== document.documentElement) return false;
      }
      return true;
    };

    const wrap = (node: Text) => {
      const parent = node.parentNode;
      if (!parent) return;
      const outer = document.createElement("font");
      outer.setAttribute("style", "vertical-align: inherit;");
      const inner = document.createElement("font");
      inner.setAttribute("style", "vertical-align: inherit;");
      parent.insertBefore(outer, node);
      outer.appendChild(inner);
      // Moves the ORIGINAL text node — the crux of the bug.
      inner.appendChild(node);
      w.__spTranslateWrapped = (w.__spTranslateWrapped ?? 0) + 1;
    };

    const scan = (root: Node) => {
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      const found: Text[] = [];
      while (walker.nextNode()) {
        if (shouldWrap(walker.currentNode)) found.push(walker.currentNode as Text);
      }
      for (const node of found) wrap(node);
    };

    scan(document.body);

    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const added of Array.from(record.addedNodes)) {
          if (shouldWrap(added)) {
            wrap(added);
          } else if (added.nodeType === Node.ELEMENT_NODE) {
            scan(added);
          }
        }
      }
    });
    observer.observe(document.body, { childList: true, subtree: true });
    w.__spTranslateObserver = observer;

    return w.__spTranslateWrapped ?? 0;
  }, mode);
}

/**
 * Every element the hardening is supposed to cover, by stable selector.
 * `seedRichStatusPage` exists so that all of these actually render.
 *
 * The two Radix tooltips (the resource status dot, the availability bars) are
 * NOT in this list: they only mount while hovered, so they are exercised
 * separately by `hoverTooltipsAreHardened` below.
 */
const HARDENED_SELECTORS = [
  '[data-testid="overall-status-badge"]',
  '[data-testid="resource-status-badge"]',
  '[data-testid="resource-maintenance-badge"]',
  '[data-testid="resource-availability-pct"]',
  '[data-testid="availability-axis"]',
  '[data-testid="status-update-kind"]',
  '[data-testid="status-update-time"]',
  '[data-testid="subscribe-submit"]',
  ".sp-version",
] as const;

/**
 * For each expected element: is it on the page, does it still carry
 * `translate="no"`, and did the simulation get a <font> into it?
 *
 * Deleting a `translate="no"` fails `hardened`. Deleting the element or letting
 * it stop rendering fails `present` — so the coverage cannot silently rot the
 * way a bare count can.
 */
async function optOutReport(page: Page, selectors: readonly string[]) {
  return page.evaluate((selectors: string[]) => {
    return {
      elements: selectors.map((selector) => {
        const el = document.querySelector(selector);
        return {
          selector,
          present: Boolean(el),
          hardened: el?.getAttribute("translate") === "no",
          wrapped: Boolean(el?.querySelector("font")),
        };
      }),
      // Operator content stays translatable on purpose — it MUST be wrapped,
      // otherwise the simulation is not doing anything and everything above
      // would pass vacuously.
      pageNameWrapped: Boolean(
        document.querySelector(".sp-page-name")?.querySelector("font"),
      ),
    };
  }, [...selectors]);
}

/**
 * The two hover-only tooltips (the resource status dot, the availability bar
 * segments). Radix mounts TooltipContent into a portal on hover, so the static
 * sweep in `optOutReport` cannot reach them.
 *
 * After each hover, EVERY tooltip currently mounted must be hardened and
 * untouched; the collected texts are returned so the caller can prove the two
 * hovers really did surface two different tooltips (Radix keeps a closing
 * tooltip's wrapper around for a moment, so "one wrapper" is not a safe proxy).
 */
async function hoverTooltipTexts(
  page: Page,
  triggers: Locator[],
): Promise<Set<string>> {
  const seen = new Set<string>();
  // Radix leaves the popper wrapper in the DOM after close (exit animation), so
  // "is a wrapper present" is not "is a tooltip open" — filter on data-state.
  const open = page.locator(
    '[data-radix-popper-content-wrapper] > [data-state]:not([data-state="closed"])',
  );

  for (const trigger of triggers) {
    // Close whatever is open first, otherwise the next hover can be swallowed
    // by the previous tooltip and we would assert on it twice.
    // Move away in STEPS. Radix keeps a tooltip open while it believes the
    // pointer is in transit from the trigger to the content, and a one-jump
    // mouse move (what locator.hover() does) never resolves that state — the
    // tooltip then stays open forever and the next hover is swallowed.
    await page.mouse.move(5, 5, { steps: 12 });
    await expect(open).toHaveCount(0, { timeout: 10000 });

    await trigger.hover();
    await expect(open.first()).toBeVisible({ timeout: 10000 });
    // Let the content settle (and the translator's observer have its go at it).
    await page.waitForTimeout(500);

    const states = await open
      .evaluateAll((els) =>
        els.map((el) => ({
          hardened: el.getAttribute("translate") === "no",
          wrapped: Boolean(el.querySelector("font")),
          text: el.textContent?.trim() ?? "",
        })),
      );

    expect(states.length, "no tooltip mounted on hover").toBeGreaterThan(0);
    for (const state of states) {
      expect(
        state.hardened,
        `tooltip "${state.text}" lost its translate="no"`,
      ).toBe(true);
      expect(
        state.wrapped,
        `tooltip "${state.text}" was translated despite translate="no"`,
      ).toBe(false);
      seen.add(state.text);
    }
  }

  return seen;
}

/**
 * Switches the UI language through the real LanguageSwitcher dropdown.
 *
 * The trigger click is retried: Radix swallows a pointerdown that lands while
 * the menu is still running its close transition, which is easy to hit when two
 * switches happen back to back.
 */
async function switchLanguage(page: Page, label: string) {
  const trigger = page.locator("header button[aria-label]").first();
  const menu = page.getByRole("menu");

  await expect(async () => {
    await trigger.click();
    await expect(menu).toBeVisible({ timeout: 2000 });
  }).toPass({ timeout: 30000 });

  await page.getByRole("menuitem", { name: label }).click();
  await expect(menu).toBeHidden({ timeout: 10000 });
}

test.describe("Public status page under Chrome auto-translate", () => {
  // A real 30 s refetch cycle is waited on, so the default 30 s is too tight.
  test.describe.configure({ timeout: 150_000 });

  /**
   * THE production-relevant test: does the app keep machine-generated text out
   * of a translator's reach?
   *
   * It seeds its own status page so that every hardened element actually
   * renders (the shipped fixtures have no sections or resources, so a test
   * pointed at them would silently cover only three of them), then checks each
   * one BY SELECTOR: present, still carrying `translate="no"`, and untouched by
   * the simulation. Deleting any single `translate="no"` fails this test, on
   * `make dev` and against the bundle CI serves alike — verified by reverting
   * one and watching it fail against the production build.
   */
  test("keeps machine-generated text out of a forced translation", async ({
    page,
    request,
  }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    let statusFetches = 0;
    page.on("request", (req) => {
      if (req.url().includes("/api/v1/status-pages/")) statusFetches += 1;
    });

    const { url } = await seedRichStatusPage(request);
    await page.goto(url);
    await expect(page.locator(".sp-page-name")).toBeVisible({ timeout: 20000 });
    // Every covered element must be on screen BEFORE the simulation runs,
    // otherwise "not wrapped" would just mean "not rendered".
    for (const selector of HARDENED_SELECTORS) {
      await expect(
        page.locator(selector).first(),
        `${selector} did not render — the seeded fixture no longer covers it`,
      ).toBeVisible({ timeout: 20000 });
    }

    // A visitor forces "Translate to…", overriding index.html's document-level
    // opt-out. Element-level opt-outs still apply — as in a real translator.
    const wrapped = await simulateChromeTranslate(page, "respectOptOut");
    expect(wrapped).toBeGreaterThan(3);

    // Then live re-renders: two language switches and a real background refetch.
    await switchLanguage(page, "Français");
    await expect
      .poll(() => page.evaluate(() => document.documentElement.lang), {
        timeout: 10000,
      })
      .toBe("fr");
    await switchLanguage(page, "Deutsch");
    await expect
      .poll(() => page.evaluate(() => document.documentElement.lang), {
        timeout: 10000,
      })
      .toBe("de");

    const before = statusFetches;
    await expect
      .poll(() => statusFetches, { timeout: 60000, intervals: [1000] })
      .toBeGreaterThan(before);
    await page.waitForTimeout(1000);

    const report = await optOutReport(page, HARDENED_SELECTORS);

    // The simulation is doing real work: operator content, which is meant to
    // stay translatable, HAS been re-parented into <font> wrappers. Without
    // this every assertion below could pass vacuously.
    expect(
      report.pageNameWrapped,
      "the simulation wrapped nothing — the assertions below prove nothing",
    ).toBe(true);

    for (const element of report.elements) {
      expect(element.present, `${element.selector} is not on the page`).toBe(
        true,
      );
      expect(
        element.hardened,
        `${element.selector} lost its translate="no"`,
      ).toBe(true);
      expect(
        element.wrapped,
        `${element.selector} was translated despite translate="no"`,
      ).toBe(false);
    }

    // The two hover-only tooltips, which the static sweep cannot reach.
    const tooltipTexts = await hoverTooltipTexts(page, [
      page.getByTestId("resource-status-dot").first(),
      page.getByTestId("availability-bar-segment").first(),
    ]);
    expect(
      tooltipTexts.size,
      `expected two distinct tooltips, saw: ${[...tooltipTexts].join(" | ")}`,
    ).toBeGreaterThanOrEqual(2);

    await expect(page.getByText(ERROR_BOUNDARY_TEXT)).toHaveCount(0);
    expect(pageErrors, `uncaught page errors:\n${pageErrors.join("\n")}`).toEqual(
      [],
    );
  });

  test("survives translated DOM across language switches and refetches", async ({
    page,
    request,
  }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    // Count background refetches so we can wait for a REAL one instead of
    // guessing at a timeout.
    let statusFetches = 0;
    page.on("request", (req) => {
      if (req.url().includes("/api/v1/status-pages/")) statusFetches += 1;
    });

    await page.goto(await resolveStatusPageUrl(request));
    await expect(page.locator(".sp-page-name")).toBeVisible({ timeout: 20000 });
    const pageName = (await page.locator(".sp-page-name").textContent())?.trim();
    expect(pageName).toBeTruthy();

    // Exactly one React root. A second createRoot() on #root mounts a second
    // copy of the app and kills the first one with the very same
    // "removeChild on Node" error — see the mount guard in main.tsx.
    await expect(page.locator(".sp-page-name")).toHaveCount(1);
    expect(
      await page.evaluate(
        () => document.getElementById("root")?.children.length ?? -1,
      ),
    ).toBe(1);
    await expect(page.getByText(ERROR_BOUNDARY_TEXT)).toHaveCount(0);
    expect(pageErrors, `uncaught errors on plain load:\n${pageErrors.join("\n")}`)
      .toEqual([]);

    // --- Hostile mutation ------------------------------------------------
    // "forced": ignores every opt-out, so even the hardened elements get
    // wrapped. Nothing in the field behaves this badly; this is the belt to the
    // previous test's braces.
    const wrapped = await simulateChromeTranslate(page, "forced");

    // Positive control #1: the simulation actually did something, and the
    // resulting DOM really is the shape that breaks React — the status page
    // name's text node is no longer a direct child of its React-owned element.
    expect(wrapped).toBeGreaterThan(5);
    const detached = await page.evaluate(() => {
      const el = document.querySelector(".sp-page-name");
      if (!el) return null;
      return {
        firstChildTag: el.firstChild?.nodeName ?? null,
        hasDirectText: Array.from(el.childNodes).some(
          (n) => n.nodeType === Node.TEXT_NODE,
        ),
        fonts: document.querySelectorAll("font").length,
      };
    });
    expect(detached).not.toBeNull();
    expect(detached!.firstChildTag).toBe("FONT");
    expect(detached!.hasDirectText).toBe(false);
    expect(detached!.fonts).toBeGreaterThan(5);

    // --- Re-render path 1: language switch --------------------------------
    await switchLanguage(page, "Français");
    await expect
      .poll(() => page.evaluate(() => document.documentElement.lang), {
        timeout: 10000,
      })
      .toBe("fr");

    await expect(page.getByText(ERROR_BOUNDARY_TEXT)).toHaveCount(0);
    await expect(page.locator(".sp-page-name")).toBeVisible();

    // Switch again — this one re-renders text that the MutationObserver has
    // meanwhile re-wrapped, which is the state Chrome leaves a live SPA in.
    await switchLanguage(page, "Deutsch");
    await expect
      .poll(() => page.evaluate(() => document.documentElement.lang), {
        timeout: 10000,
      })
      .toBe("de");

    // --- Re-render path 2: the 30 s background refetch ---------------------
    const before = statusFetches;
    await expect
      .poll(() => statusFetches, { timeout: 60000, intervals: [1000] })
      .toBeGreaterThan(before);
    // Let the resulting commit (and the observer's re-wrap) settle.
    await page.waitForTimeout(1000);

    // --- Assertions --------------------------------------------------------
    await expect(page.getByText(ERROR_BOUNDARY_TEXT)).toHaveCount(0);
    await expect(page.locator(".sp-page-name")).toBeVisible();
    await expect(page.locator(".sp-footer")).toBeVisible();
    // Content survived: the header still carries the same page name.
    expect((await page.locator(".sp-page-name").textContent())?.trim()).toBe(
      pageName,
    );
    // The mitigation in main.tsx still holds after all of the above.
    expect(await page.evaluate(() => document.documentElement.lang)).toBe("de");

    expect(
      pageErrors,
      `uncaught page errors after simulated auto-translate:\n${pageErrors.join("\n")}`,
    ).toEqual([]);
  });

  test("positive control: the pageerror harness catches uncaught errors", async ({
    page,
    request,
  }) => {
    // Guards the test above from becoming vacuous: if Playwright ever stopped
    // surfacing uncaught errors, the "no removeChild crash" assertion would
    // pass for the wrong reason.
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    await page.goto(await resolveStatusPageUrl(request));
    await expect(page.locator(".sp-page-name")).toBeVisible({ timeout: 20000 });

    await page.evaluate(() => {
      setTimeout(() => {
        throw new Error("sp-e2e-control: uncaught");
      }, 0);
    });

    await expect
      .poll(() => pageErrors.join("|"), { timeout: 10000 })
      .toContain("sp-e2e-control");
  });

  test("positive control: a removeChild on a re-parented text node throws", async ({
    page,
    request,
  }) => {
    // Proves the simulated DOM shape is genuinely the one that crashes React:
    // asking the ORIGINAL parent to remove the (now re-parented) text node
    // throws exactly the NotFoundError users were reporting.
    await page.goto(await resolveStatusPageUrl(request));
    await expect(page.locator(".sp-page-name")).toBeVisible({ timeout: 20000 });
    await simulateChromeTranslate(page);

    const thrown = await page.evaluate(() => {
      const el = document.querySelector(".sp-page-name");
      const text = el?.querySelector("font font")?.firstChild;
      if (!el || !text) return "no-fixture";
      try {
        el.removeChild(text);
        return "no-throw";
      } catch (err) {
        return String((err as Error).message);
      }
    });

    expect(thrown).toContain("removeChild");
  });
});
