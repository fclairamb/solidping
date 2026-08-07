---
model: sonnet
effort: medium
---

# status0 E2E specs hardcode localhost:4000 and cannot target a side-car test server

## Problem

Four Playwright specs under `web/status0/e2e/` hardcode
`const BASE = "http://localhost:4000"` with no env var at all, so status0's
E2E suite cannot target a side-car test server on a non-default port:

- `web/status0/e2e/status-page.spec.ts:3`
- `web/status0/e2e/maintenance-badge.spec.ts:3`
- `web/status0/e2e/status-updates.spec.ts:13`
- `web/status0/e2e/subscribe.spec.ts:3`

A fifth spec, `web/status0/e2e/translate-resilience.spec.ts:48-50`, already
derives `BASE` from `E2E_BASE_URL` inline — but keeps its own
`"http://localhost:4000"` fallback literal, which duplicates the logic and
would trip the regression guard below. It should migrate to the shared module
like the others.

This was recorded as an explicit out-of-scope follow-up ("Tier C") in
`specs/done/2026/08/2026-08-06-01-e2e-hardcoded-base-url.md` §5, whose
Tier-A/Tier-B fix for `web/dash0/e2e/` has now landed. Use that spec and the
resulting dash0 code as the model.

## Proposal

### 1. Introduce a canonical base module

Unlike dash0, status0 has no `web/status0/e2e/fixtures.ts` to import from.
Introduce one (or a small shared `base.ts`) that derives the origin from
`E2E_BASE_URL` exactly like `web/dash0/e2e/fixtures.ts:14-16` does:

```ts
export const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";
```

Migrate all five spec files to import from it (the four hardcoded ones plus
`translate-resilience.spec.ts`'s inline derivation).

### 2. Port the ESLint regression guard

`web/dash0/eslint.config.js:35-57` gained an override block scoped to
`files: ["e2e/**/*.ts"]` with `ignores: ["e2e/fixtures.ts"]` and
`no-restricted-syntax` selectors banning:

- `process.env.E2E_API_BASE` (the dead-config MemberExpression selector)
- `Literal[value=/localhost:4000/]`
- `TemplateElement[value.raw=/localhost:4000/]`

Add the equivalent block to `web/status0/eslint.config.js`, exempting
whichever file becomes the canonical base module.

**Verify the guard actually fires**: temporarily reintroduce a hardcoded
`localhost:4000` literal in a spec, confirm eslint errors on it, then revert.
A guard only ever run against clean code proves nothing.

## QA

Per the repo's scoped-target convention — do **not** run `make build` or
`make ci`:

1. `make build-status0`
2. `cd web/status0 && bun run lint` — gate is **no NEW eslint errors in
   touched files**.
3. Verify on a non-default port with a side-car server:

```bash
make build-status0 copy-status0 build-backend
PORT=4321 SP_RUNMODE=test SP_DB_RESET=true ./solidping serve &
cd web/status0 && E2E_BASE_URL=http://localhost:4321/ CI=true bunx playwright test --retries=0
```

Run playwright from `web/status0`, not the repo root.
