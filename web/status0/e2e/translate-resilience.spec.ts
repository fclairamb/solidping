import { test, expect, type APIRequestContext, type Page } from "@playwright/test";

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
 *     The simulation below honours element-level opt-outs exactly as a real
 *     translator does, so "the poll-driven text was never wrapped" is a real
 *     assertion — delete a `translate="no"` and this spec fails.
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

// Honors E2E_BASE_URL so this can be pointed at a side-car / CI server rather
// than only the :4000 dev loop.
const BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";

/** TanStack Router's default error boundary copy. */
const ERROR_BOUNDARY_TEXT = "Something went wrong!";

/**
 * Finds a public status page to exercise. `make dev` seeds the `default` org,
 * `SP_RUNMODE=test` (CI) seeds `test` instead, so probe rather than hardcode.
 */
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
 * Reports, for every element the app opted out of translation, whether the
 * simulation leaked a <font> into it — plus a control element that is NOT
 * opted out, so an empty result can be told apart from a simulation that did
 * nothing.
 */
async function optOutReport(page: Page) {
  return page.evaluate(() => {
    const hardened = Array.from(
      document.querySelectorAll('#root [translate="no"], [translate="no"]'),
    ).filter((el) => el !== document.documentElement);

    return {
      hardenedCount: hardened.length,
      leaked: hardened
        .filter((el) => el.querySelector("font"))
        .map((el) => el.getAttribute("data-testid") || el.className || el.tagName),
      // Operator content stays translatable on purpose — it must be wrapped,
      // otherwise the simulation is not doing anything and "no leaks" is
      // meaningless.
      pageNameWrapped: Boolean(
        document.querySelector(".sp-page-name")?.querySelector("font"),
      ),
      statusBadgeHardened: document
        .querySelector('[data-testid="overall-status-badge"]')
        ?.getAttribute("translate"),
      versionHardened: document
        .querySelector(".sp-version")
        ?.getAttribute("translate"),
    };
  });
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
   * THE production-relevant test: does the app keep the poll-driven text out of
   * a translator's reach?
   *
   * This runs identically against `make dev` and the built bundle CI serves, and
   * it fails if any `translate="no"` marker added by spec 2026-08-01-05 is
   * removed — the simulation would then re-parent that text into a <font>, which
   * is precisely the state React later trips over.
   */
  test("keeps poll-driven text out of a forced translation", async ({
    page,
    request,
  }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    let statusFetches = 0;
    page.on("request", (req) => {
      if (req.url().includes("/api/v1/status-pages/")) statusFetches += 1;
    });

    await page.goto(await resolveStatusPageUrl(request));
    await expect(page.locator(".sp-page-name")).toBeVisible({ timeout: 20000 });
    await expect(
      page.getByTestId("overall-status-badge"),
    ).toBeVisible({ timeout: 20000 });
    await expect(page.locator(".sp-version")).toBeVisible({ timeout: 20000 });

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

    const report = await optOutReport(page);

    // The markers exist at all. Removing any `translate="no"` added by spec
    // 2026-08-01-05 drops this count — verified by reverting one and watching
    // this line fail against the production bundle.
    expect(report.hardenedCount).toBeGreaterThanOrEqual(3);
    expect(report.statusBadgeHardened).toBe("no");
    expect(report.versionHardened).toBe("no");

    // The simulation is doing real work: operator content, which is meant to
    // stay translatable, HAS been re-parented into <font> wrappers. Without
    // this the "no leaks" assertion below could pass vacuously.
    expect(report.pageNameWrapped).toBe(true);

    // …and not one opted-out element was touched, before or after the
    // re-renders. This is the property that makes the crash structurally
    // impossible at the sites React rewrites on every poll.
    expect(
      report.leaked,
      `translator leaked into opted-out elements: ${report.leaked.join(", ")}`,
    ).toEqual([]);

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
