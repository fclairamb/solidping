---
model: sonnet
effort: medium
---

# E2E and showcase specs locate elements by text, role or label: move them to `data-testid`

## Problem
`web/dash0/src` already carries 1827 `data-testid` and the e2e suite uses `getByTestId`/`data-testid` 3757 times. But 1218 locators in `web/dash0/e2e/` are still `getByText`, `getByRole(..., { name })`, `getByLabel`, `getByPlaceholder`, `:has-text` or `text=` (627 are `getByRole` with a `name`). The five heaviest files are `discovery.spec.ts` (87), `check-detail.spec.ts` (83), `checks.spec.ts` (79), `integrations.spec.ts` (58) and `incidents.spec.ts` (44). The showcase pipeline (`web/dash0/showcase/specs/*.showcase.ts`, which films the product videos) has 20 such locators against 58 testid ones, e.g. `sms-opt-in-consent.showcase.ts:76,108,109` and `setup-to-first-result.showcase.ts:192`.

A copy change, a rename or a new locale (dash0 ships `en`, `fr`, `de`, `es` in `src/locales/`) breaks a text locator without any behaviour change. A video re-cut breaks the same way. A testid is stable and says what the element is.

Where the instinct "testid everywhere, nothing from text" goes too far:
- Testids on non-interactive elements (layout wrappers, icons) are noise. The `src` rule below covers interactive elements only.
- `getByRole` is not banned (decided 2026-10-25): a role+name locator fails when a button has no accessible name, a free accessibility check worth keeping.
- Locating by testid does not mean never asserting text. `await expect(page.getByTestId("check-status")).toHaveText("Up")` is the right form: locate by testid, assert what the user sees.

## Proposal
1. Rule in `web/dash0/AGENTS.md` (e2e section): locate with `getByTestId`. `getByText`, `getByPlaceholder`, `getByLabel`, `:has-text`, `text=` are not used as locators in `e2e/` or `showcase/`. Text stays as an assertion on a testid-located element. `getByRole` stays allowed. Prefer `getByTestId` when both fit.
2. Enforce it with an ESLint rule scoped to `e2e/**` and `showcase/**` (`web/dash0/eslint.config.js`, custom rule next to `eslint-rules/no-untranslated-jsx-text.js`, with a test like `no-untranslated-jsx-text.test.js`). Start with the existing violations baselined (ESLint bulk suppressions or a per-file allowlist), so the rule bites on new code immediately and the baseline only shrinks.
3. Migrate the showcase specs first (20 locators, they decide what ends up on camera and in the docs): add the missing testids in `web/dash0/src`, switch the locators, regenerate nothing (the output must not change visually).
4. Migrate `e2e/` file by file, heaviest first (list above), removing each file from the baseline as it is done. One PR per few files, each green on its own.
5. For each locator moved, add the `data-testid` in `src` on the element it targets, kebab-case and descriptive (`save-button`), per the repo-wide frontend rule.
6. ESLint rule on `src/**/*.tsx` (same `eslint-rules/` folder): an interactive element without a `data-testid` is reported. Interactive means `<Button>`, `<Input>`, `<Textarea>`, `<Select>` trigger, `<Checkbox>`, `<Switch>`, `<a>`, `<Link>`, `<form>`, dialog and alert-dialog content, and native `<button>`, `<input>`, `<select>`, `<textarea>`. A spread prop (`{...props}`) counts as satisfied, so the shared primitives in `src/components/ui/` forward the id from their callers instead of hard-coding one. Baseline the existing violations the same way as step 2: new code is caught at once, the baseline only shrinks.
7. After a dash0 change, the server embeds the build (`server/internal/app/dash0res/`): check the embed contains the new ids before trusting a red run (`wiki/testing/local-e2e.md:49`).

## Tests
- `eslint-rules/<src-rule>.test.js`: `<Button>Save</Button>` is reported; `<Button data-testid="save-button">` is not; `<Button {...props}>` is not; a `<div onClick>` is not (out of scope); a file under `e2e/` is not checked.
- `eslint-rules/<new-rule>.test.js`: `getByText("Save")` in `e2e/` is reported; `expect(page.getByTestId("x")).toHaveText("Save")` is not; `getByRole` is not reported; a file under `src/` is not checked.
- Negative case: a spec with a baselined violation passes lint, the same line in a new file fails.
- `bunx playwright test` on every migrated spec stays green with no change in assertions' meaning (a migrated test must still fail if the text it asserts changes).
- Showcase: `make showcase` against a fresh side-car (`wiki/features/showcase-media.md`) produces the same four beats after migration.

## To verify
- How many interactive elements in `web/dash0/src` have no `data-testid` today: this sizes the step 6 baseline.
- Whether any e2e spec asserts accessible names on purpose (the 627 `getByRole` with `name`). Those stay as they are.
- Whether testids are stripped from the production build. If not, check the size cost.
