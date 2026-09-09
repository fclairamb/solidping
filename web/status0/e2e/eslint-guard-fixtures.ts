/**
 * Positive controls for `e2e-local/no-node-scope-in-browser-callback`
 * (spec 2026-09-09-03).
 *
 * Every browser callback below is written CORRECTLY — the Node-side value is
 * handed in as the `evaluate`/`$eval`/… argument rather than closed over — so
 * this file must stay green under `bun run lint:e2e` and `bun run typecheck:e2e`.
 * If a future tightening of the rule turns it red, the rule has become "reject
 * everything" and the tightening is wrong.
 *
 * It is deliberately NOT a `*.spec.ts`, so Playwright's default `testMatch`
 * never collects it: it exists to be linted and type-checked, not run.
 */
import type { Page } from "@playwright/test";

// A Node-side module binding, exactly the shape that bit us in 1dca14ba8.
const NODE_SIDE_BASE = "/d";

export async function correctBrowserCallbacks(page: Page): Promise<void> {
  // 1. The value travels as the evaluate argument, not through the closure.
  await page.evaluate(
    (base) => navigator.serviceWorker.getRegistration(`${base}/sw.js`),
    NODE_SIDE_BASE,
  );

  // 2. Browser globals are always fine — they exist in the page.
  await page.evaluate(() => {
    window.localStorage.clear();
    return document.title + navigator.userAgent + String(self.origin);
  });

  // 3. The callback's own parameters and locals are fine.
  await page.evaluate((prefix: string) => {
    const key = `${prefix}:seen`;
    const read = (name: string) => window.localStorage.getItem(name);
    return read(key);
  }, NODE_SIDE_BASE);

  // 4. Type-only references are erased before serialization.
  await page
    .getByTestId("email-recipients-input")
    .evaluate((el, text) => {
      const input = el as HTMLInputElement;
      input.value = text;
    }, "a@acme.com");

  // 5. The rest of the family, all passing their value in as an argument.
  await page.evaluateHandle((base) => document.querySelector(`a[href^="${base}"]`), NODE_SIDE_BASE);
  await page.$eval("html", (el, base) => el.getAttribute(base), NODE_SIDE_BASE);
  await page.$$eval("a", (els, base) => els.filter((a) => a.href.includes(base)).length, NODE_SIDE_BASE);
  await page.waitForFunction((base) => window.location.pathname.startsWith(base), NODE_SIDE_BASE);
  await page.addInitScript((base) => {
    window.localStorage.setItem("base", base);
  }, NODE_SIDE_BASE);
  await page.context().addInitScript((base) => {
    window.localStorage.setItem("ctx-base", base);
  }, NODE_SIDE_BASE);
}
