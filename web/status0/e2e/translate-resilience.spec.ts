import { test, expect, type Page } from "@playwright/test";

/**
 * Regression guard for the recurring
 *   "Failed to execute 'removeChild' on 'Node'"
 * crash on the public status page (surfaced to visitors as TanStack Router's
 * default "Something went wrong!" screen).
 *
 * Root cause: Chrome's auto-translate re-parents every text node into nested
 * <font style="vertical-align: inherit"> wrappers, *keeping the original text
 * node object*. React's fiber tree still records the ORIGINAL parent for that
 * text node, so the next commit that removes it calls
 * `originalParent.removeChild(textNode)` and the DOM throws NotFoundError,
 * because the node now lives under a <font>.
 *
 * The tests below reproduce that DOM shape (including Chrome's MutationObserver
 * behaviour of re-translating anything React renders afterwards) and then drive
 * the two re-render paths that historically crashed: switching the UI language
 * and the 30 s background refetch.
 */

const BASE = "http://localhost:4000";
const STATUS_PAGE = `${BASE}/status0/default`;

/** TanStack Router's default error boundary copy. */
const ERROR_BOUNDARY_TEXT = "Something went wrong!";

/**
 * Installs a faithful simulation of Chrome auto-translate:
 *
 *  - every visible HTML text node is MOVED into nested <font> wrappers (the
 *    same node object, exactly like Chrome — this is what breaks React), and
 *  - a MutationObserver keeps doing it to anything rendered later, which is how
 *    Chrome keeps a live SPA translated.
 *
 * SVG text is skipped: Chrome does not translate it, and wrapping it would
 * manufacture a failure mode that cannot happen in the field.
 */
async function simulateChromeTranslate(page: Page): Promise<number> {
  return page.evaluate(() => {
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

  test("survives translated DOM across language switches and refetches", async ({
    page,
  }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    // Count background refetches so we can wait for a REAL one instead of
    // guessing at a timeout.
    let statusFetches = 0;
    page.on("request", (request) => {
      if (request.url().includes("/api/v1/status-pages/")) statusFetches += 1;
    });

    await page.goto(STATUS_PAGE);
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
    const wrapped = await simulateChromeTranslate(page);

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
  }) => {
    // Guards the test above from becoming vacuous: if Playwright ever stopped
    // surfacing uncaught errors, the "no removeChild crash" assertion would
    // pass for the wrong reason.
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    await page.goto(STATUS_PAGE);
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
  }) => {
    // Proves the simulated DOM shape is genuinely the one that crashes React:
    // asking the ORIGINAL parent to remove the (now re-parented) text node
    // throws exactly the NotFoundError users were reporting.
    await page.goto(STATUS_PAGE);
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
