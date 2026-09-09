---
model: opus
effort: high
---

# Nothing type-checks the Playwright e2e suites, so a `page.evaluate` closing over a Node-side identifier ships green

## Problem

**No tsconfig project covers `web/dash0/e2e/` or `web/status0/e2e/`.**

- `web/dash0/tsconfig.app.json:41` — `"include": ["src"]`
- `web/dash0/tsconfig.node.json:20` — `"include": ["vite.config.ts"]`
- `web/dash0/tsconfig.json` references only those two projects, so `bun run build`
  (`tsc -b && vite build`) never sees `e2e/`. Same shape in
  `web/status0/tsconfig.{app,node,}.json`.

That is 159 spec/fixture files in `web/dash0/e2e/` and 14 in `web/status0/e2e/`
with zero type-checking anywhere — not locally, not in CI.

### How it bit us

Spec `2026-09-09-01` (retire legacy dash and shorten SPA base paths, now in
`specs/done/2026/09/`) ran a mechanical path sweep that rewrote
`web/dash0/e2e/webpush.spec.ts` into:

```ts
page.evaluate(() => navigator.serviceWorker.getRegistration(`${DASH_BASE}/sw.js`))
```

`DASH_BASE` is imported from `./fixtures` and lives in the **Node** process.
Playwright serializes the callback and evaluates it **in the page**, where
`DASH_BASE` is not in scope — a `ReferenceError` at runtime. ESLint passed. There
is no type-check over `e2e/`. It stayed invisible until a human read the diff, and
was fixed by passing the value as the `page.evaluate` argument (commit
`1dca14ba8`).

This is a *class* of bug, not a one-off: `grep` finds `.evaluate(` /
`.evaluateHandle(` / `$eval` / `$$eval` / `waitForFunction` / `addInitScript`
callbacks across ~30+ dash0 e2e files. Every one is a place a future sweep can
close over a module-scope import and ship a green PR.

### Two aggravating facts found while filing this

1. **The `dash0` CI job has no lint step at all.** `.github/workflows/ci.yml:166`
   runs install → `test:unit` → `build` → upload. Only the `status0` job runs
   `bun run lint` (`ci.yml:226`), and its comment on `ci.yml:224` refers to "the
   `dash` job's lint step" — the legacy `dash` job that spec `2026-09-09-01`
   retired. So dash0's ESLint config, **including the existing e2e
   `no-restricted-syntax` guard** at `web/dash0/eslint.config.js:27-58`, is
   currently enforced by nothing on PRs.
2. `bun run lint` (`eslint .`) in `web/dash0` is red on base: **42 errors + 447
   warnings**, effectively all in `src/` (see the `dash0 eslint debt` note). A
   naive "add `bun run lint` to the dash0 job" lands red on day one.

   Scoped, it is tiny: `bunx eslint e2e` in `web/dash0` = **2 errors**
   (`no-unused-vars` in `notification-detail.spec.ts:4` and one more);
   `bunx eslint e2e` in `web/status0` = **0**.

### Pre-existing type errors (measured, not guessed)

Probing with a throwaway `tsconfig` over `e2e` + `playwright.config.ts`
(`strict`, `noUnusedLocals`, `noUnusedParameters`, `types: ["node"]`,
`skipLibCheck`):

| Project | Errors | Files |
|---|---|---|
| `web/dash0` | 46 | 5 |
| `web/status0` | 0 | — |

dash0's breakdown:

| File | Errors |
|---|---|
| `e2e/check-result-detail-navigation.spec.ts` | 22 |
| `e2e/check-chart-point-preview.spec.ts` | 21 |
| `e2e/notification-detail.spec.ts` | 1 |
| `e2e/integrations.spec.ts` | 1 |
| `e2e/account-notifications.spec.ts` | 1 |

The two 20-error files are **the same real bug**, and it is exactly the kind this
spec exists to catch: a helper takes the whole Playwright fixtures object where it
should destructure `{ page }`, so every `.getByTestId` / `.waitForURL` /
`.waitForLoadState` call on it is `TS2339 Property … does not exist on type
'PlaywrightTestArgs & …'`. It happens to work at runtime only where the value
passed in is actually a `Page`. The remaining three are unused locals.

46 errors across 5 files is small enough to **fix, not defer**.

## Proposal

### 1. Add an e2e tsconfig project to each app

`web/dash0/tsconfig.e2e.json` and `web/status0/tsconfig.e2e.json`:

- `include`: `["e2e", "playwright.config.ts"]` (dash0 also has
  `playwright.dev.config.ts` — include it).
- `lib`: `["ES2023", "DOM", "DOM.Iterable"]` — the DOM lib is required, since
  `page.evaluate` callbacks legitimately reference `navigator`, `document`,
  `window`.
- `types`: `["node"]` (specs read `process.env`, use `Buffer`/`path`).
- Mirror `tsconfig.node.json`'s strictness (`strict`, `noUnusedLocals`,
  `noUnusedParameters`, `noFallthroughCasesInSwitch`, `noEmit`,
  `moduleResolution: "bundler"`, `isolatedModules`, `moduleDetection: "force"`,
  `skipLibCheck`), plus its own `tsBuildInfoFile`.
- Keep the `@/*` path alias only if any e2e file actually imports through it;
  otherwise leave it out.

Reference it from the solution tsconfig (`web/dash0/tsconfig.json`,
`web/status0/tsconfig.json`) alongside the existing two projects, so plain
`tsc -b` picks it up.

**Decide deliberately whether `tsc -b` (and therefore `bun run build`) should
build the e2e project.** Either is defensible — folding it into `build` means
one command covers everything, but it also couples the production bundle build
to test-only type health. If it is kept out of `build`, it MUST be wired into CI
separately (below); do not let it end up run by nothing.

### 2. Wire it into the package scripts and CI

- Add `"typecheck:e2e": "tsc -b tsconfig.e2e.json"` (or `-p`, matching whichever
  reference wiring step 1 chose) to both `package.json`s. A general
  `"typecheck": "tsc -b"` is fine too.
- Add `"lint:e2e": "eslint e2e"` to `web/dash0/package.json` — scoped on purpose,
  because `eslint .` is red on base and fixing that debt is **out of scope here**
  (record it as debt; do not fold it in).
- `.github/workflows/ci.yml`, `dash0` job (`ci.yml:166`): add a **Type-check e2e**
  step and a **Lint e2e** step (`bun run lint:e2e`), with a comment saying why the
  lint step is scoped to `e2e` rather than running `bun run lint`, and pointing at
  the `src/` ESLint debt.
- `.github/workflows/ci.yml`, `status0` job (`ci.yml:199`): add the type-check
  step. Its `bun run lint` already covers `e2e` (it is a bare `eslint .`).
- Fix the stale `ci.yml:224` comment that refers to the retired `dash` job.

### 3. Prove the check fails on the motivating bug — this is the acceptance gate

**`tsc` alone will almost certainly NOT catch it.** Playwright types the
`evaluate` callback, but a bare closure over an in-scope module import is
perfectly legal TypeScript; the compiler has no idea the function will be
serialized and shipped to another realm. Assume the type-check is necessary but
insufficient, and verify rather than assume:

1. Temporarily reintroduce the exact `1dca14ba8` shape in
   `web/dash0/e2e/webpush.spec.ts` —
   `page.evaluate(() => navigator.serviceWorker.getRegistration(\`${DASH_BASE}/sw.js\`))`
   with `DASH_BASE` imported from `./fixtures`.
2. Run the new type-check. Record the actual output in the PR description.
3. If it passes (expected), add the ESLint rule that does catch it, scoped to
   `e2e/**/*.ts` in `web/dash0/eslint.config.js` and `web/status0/eslint.config.js`.

   The existing e2e-scoped `no-restricted-syntax` block at
   `web/dash0/eslint.config.js:27-58` (the `E2E_API_BASE` / `localhost:4000`
   regression guard from spec `2026-08-06-01`) is the precedent to follow — but
   note a plain `no-restricted-syntax` esquery selector **cannot do scope
   analysis**, and scope analysis is what this needs: the rule must flag an
   identifier reference inside the callback that resolves to a binding *outside*
   the callback, while allowing the callback's own parameters, its own locals,
   and browser globals (`window`, `document`, `navigator`, `localStorage`, …).
   That most likely means a **small local ESLint rule** (a flat-config inline
   plugin is fine — no new package) walking the callback's scope via
   `sourceCode.getScope(node)` and reporting `through`/unresolved-to-inner
   references. Cover the whole family: `evaluate`, `evaluateHandle`, `$eval`,
   `$$eval`, `waitForFunction`, `addInitScript`, on `page`, `locator` and
   `context` receivers.
4. Re-run and confirm the rule reports an **error** on the reintroduced line.
   Record that output too.
5. Revert the reintroduced bug and confirm both checks are green.

**Do not ship a check that cannot fail on the motivating example.** If the final
state is "tsconfig added, CI wired, but nothing in the repo would have caught
`DASH_BASE`", the spec is not done.

Add a couple of positive-control fixtures if practical (a spec file that *does*
correctly pass the value as an `evaluate` argument must stay green) so the rule
is not just "reject everything".

### 4. Fix the 46 pre-existing dash0 errors

They are 5 files and two distinct causes:

- `check-result-detail-navigation.spec.ts` and `check-chart-point-preview.spec.ts`
  — fix the helper signature so it takes a `Page` (or destructures `{ page }`)
  instead of the whole fixtures object. **Run both specs afterwards** to confirm
  the fix is behaviour-preserving; do not just satisfy the compiler.
- `notification-detail.spec.ts`, `integrations.spec.ts`,
  `account-notifications.spec.ts` — unused locals. Remove them, or use them if
  their absence points at a dropped assertion (check `FLAT_NOTIF_RE` in
  `notification-detail.spec.ts:4` — an unused regex constant often means an
  assertion was deleted).

Also clear the 2 `bunx eslint e2e` errors in dash0 so the new `lint:e2e` step is
green on landing.

If (contrary to the measurement above) the error count balloons once the real
config is in place, scope the check so it lands green — but scope it *explicitly*
(a listed `exclude` with a `TODO`, not a loosened `strict`) and record the debt in
`wiki/`.

### Out of scope

- The ~40 `react-hooks` errors and ~447 warnings under `web/dash0/src/`. That
  debt is why the dash0 job has no lint step; paying it down is its own spec.
- `src/**/*.test.ts(x)` unit-test type coverage, deliberately excluded from
  `tsconfig.app.json` in both apps for bun-types/DOM global collision reasons.

### Documentation

Note the new e2e project and the `page.evaluate` rule in `web/dash0/CLAUDE.md`
(and `web/status0`'s, if it has one) so the next mechanical sweep knows the guard
exists and why.

## Resolved open questions

> **Decide deliberately whether `tsc -b` (and therefore `bun run build`) should
> build the e2e project.** Either is defensible — folding it into `build` means
> one command covers everything, but it also couples the production bundle build
> to test-only type health. If it is kept out of `build`, it MUST be wired into
> CI separately (below); do not let it end up run by nothing.

**Resolved — keep it OUT of `build`.** Do **not** reference `tsconfig.e2e.json`
from the solution `tsconfig.json` that `bun run build` (`tsc -b && vite build`)
resolves. Instead:

- Add a `"typecheck:e2e": "tsc -p tsconfig.e2e.json"` script to both
  `web/dash0/package.json` and `web/status0/package.json`, invoked directly
  against the e2e project (not via a solution-level `tsc -b`).
- Wire it as its **own** CI step in BOTH the `dash0` and `status0` jobs — this is
  mandatory, not optional. The whole point of the spec is that nothing currently
  runs; a check nothing invokes is the bug being fixed, not a fix.

Rationale: a type error in a Playwright spec must fail CI loudly, but must never
block a production bundle build or a deploy — the e2e tree ships to nobody. This
also matches how the repo already separates build / lint / test into distinct CI
steps rather than one omnibus command.

### Verified while resolving these (do not re-investigate)

- **The `@/*` path alias is NOT needed** in either `tsconfig.e2e.json`:
  `grep -rn 'from "@/' web/dash0/e2e web/status0/e2e` returns nothing. Omit it,
  per §1's "otherwise leave it out".
- **The dash0 CI job really does have no lint step.** Confirmed at
  `.github/workflows/ci.yml` — the `Dash0 Build` job is install → `test:unit` →
  `build` → upload dist, with no `eslint` anywhere. So the existing e2e-scoped
  `no-restricted-syntax` guard in `web/dash0/eslint.config.js:27-58` is currently
  enforced by nothing on PRs, exactly as §"Two aggravating facts" claims.
- **`bunx eslint e2e` in `web/dash0` is exactly 2 errors**, as measured in the
  spec. `web/status0` is 0.
