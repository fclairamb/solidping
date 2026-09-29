# dash0 e2e type-checking and lint

Read when touching `web/dash0/e2e/`, its tsconfig or the `page.evaluate` lint rule.
`tsconfig.app.json` is `include: ["src"]` and `tsconfig.node.json` is
`include: ["vite.config.ts"]`, so until this spec **nothing type-checked
`e2e/`** — 159 spec and fixture files compiled nowhere, locally or in CI.

- **`tsconfig.e2e.json`** covers `e2e/`, `playwright.config.ts` and
  `playwright.dev.config.ts`, with `tsconfig.node.json`'s strictness plus
  `lib: ["ES2023", "DOM", "DOM.Iterable"]` (browser callbacks legitimately
  reference `window`/`document`/`navigator`) and `types: ["node"]`.
  It is **deliberately not referenced** from `tsconfig.json`: a type error in a
  Playwright spec must fail CI loudly but must never block the production bundle
  build. CI runs it as its own step (`bun run typecheck:e2e`) in both the `dash0`
  and `status0` jobs.
- **`bun run lint:e2e`** (`eslint e2e`) is the dash0 job's lint step. It is
  scoped on purpose: the unscoped `bun run lint` is red on base with ~39 errors
  and ~440 warnings, effectively all `react-hooks` findings under `src/`. Paying
  that debt down is its own spec — do not relax the config to make `lint` green,
  and do not widen the CI step until the debt is gone.

### The `page.evaluate` guard

Playwright **serializes** the callback passed to `evaluate`, `evaluateHandle`,
`$eval`, `$$eval`, `waitForFunction` and `addInitScript` and runs it **in the
browser**. Anything it closes over lives in the Node process and is not there at
runtime — the page throws `ReferenceError`. `tsc` cannot see this (closing over
an in-scope import is legal TypeScript) and neither can an esquery
`no-restricted-syntax` selector (it cannot do scope analysis).

A mechanical base-path sweep shipped exactly that bug green:

```ts
// WRONG — DASH_BASE is a Node-side import; the page has never heard of it.
await page.evaluate(() => navigator.serviceWorker.getRegistration(`${DASH_BASE}/sw.js`));

// RIGHT — hand the value in as the evaluate argument.
await page.evaluate((base) => navigator.serviceWorker.getRegistration(`${base}/sw.js`), DASH_BASE);
```

`eslint-rules/no-node-scope-in-browser-callback.js` (a local flat-config plugin,
no npm package, duplicated verbatim in `web/status0` — keep the copies in sync)
walks each callback's scope and reports any reference resolving to a binding
outside it. The callback's own parameters and locals, browser globals and
type-only references are all allowed. `e2e/eslint-guard-fixtures.ts` is the
positive control: correctly-written calls that must stay green, so the rule can
never degrade into "reject everything". It is not a `*.spec.ts`, so Playwright
never collects it.
