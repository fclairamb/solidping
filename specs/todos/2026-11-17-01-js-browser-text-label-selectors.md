---
model: sonnet
effort: medium
pr: pingdom-tms
automerge: false
---

# JS browser checks only accept CSS selectors; let scripts target elements by text, label and name

## Problem
Every `page.*` method of the JS check's browser session takes a CSS selector, resolved with `chromedp.ByQuery` (`server/internal/checkers/checkbrowser/session.go:526-590`: `WaitVisible`, `Click`, `Fill`, `Press`, `Text`). Real-world flows are easier to write, and survive HTML refactors better, when they target what a human sees: the button text, the field's `<label>`, its `name`.

It is also the blocker for importing Pingdom transaction checks (spec `pingdom-tms-import` in this PR): Pingdom's step vocabulary takes "label, name or CSS selector" in one string (`"element": "START FREE TRIAL"`), so a 1:1 translation needs the engine to resolve that.

The JS surface also lacks three actions Pingdom "script" checks use: check/uncheck a checkbox, select a dropdown option, submit a form.

## Proposal
1. **Selector prefixes**, Playwright-style, parsed in one place in `checkbrowser` (new `selector.go`):
   - no prefix, or `css=`: CSS, exactly as today (backward compatible: `"button"` keeps meaning the CSS tag);
   - `text=`: visible text of a clickable / text element (button, link, `[role=button]`, `input[type=submit|button]` by its `value`, then any element whose own text matches);
   - `label=`: the form control associated with a `<label>` (via `for=`, nesting, or `aria-label` / `aria-labelledby`), also `placeholder` as last resort;
   - `name=`: `[name="…"]`;
   - `auto=`: try CSS (if it parses), then `name=`, then `label=`, then `text=`, first strategy that yields a match wins. Meant for imports, documented as such.
2. **Resolution**: a JS function evaluated in the page finds the matching elements, keeps only **visible** ones, requires **exactly one**, tags it with a per-call attribute (`data-sp-target="<nonce>"`), and returns the CSS selector `[data-sp-target="<nonce>"]`; the existing `chromedp.ByQuery` actions then run unchanged. The attribute is removed after the action (best effort).
   - Text matching: whitespace-normalised, case-insensitive; exact match first, then "contains" only if exact found nothing.
   - 0 matches: error `no element matches text="…"`; >1: error `text="…" matches 3 elements, make it more specific`. Never click the first of several.
   - `waitFor` with a non-CSS selector polls the resolver until visible or the call's timeout.
3. Apply the resolver to every selector-taking method of `Session` (`session.go:526-590`) and so to every `page.*` binding in `server/internal/checkers/checkjs/browser.go:192-250`.
4. **New page methods** in `browser.go` + `Session` (and the `BrowserSession` interface, `browser.go:47-70`):
   - `page.check(sel)` / `page.uncheck(sel)`: click only if the state differs, then assert the state;
   - `page.select(sel, option)`: option matched by value, then visible text; dispatch `input` + `change` (frameworks listen to them, same reasoning as `Fill`, `session.go:536-543`);
   - `page.submit(sel)`: `requestSubmit()` on the form (or the form owning `sel`), so validation and submit handlers run.
5. Document the prefixes and the new methods wherever the JS `browser` API is documented (the docs examples are tested by `checkjs/docs_examples_test.go`), with one login example using `label=` and `text=`.

## Tests
- `checkbrowser` (E2E-tagged, real Chrome, like the existing session tests): each prefix on a fixture page; `text=` exact beats contains; hidden duplicate ignored; ambiguous match → error naming the count; no match → error; `auto=` order; unprefixed `"h1"` still behaves as CSS (regression for the inline `<style>` case, `session.go:520`).
- `checkjs/browser_test.go` (fake session): new bindings pass selector/option through; `check` on an already-checked box does not click.
- `select` on a React-controlled `<select>` fixture fires `change`.

## To verify
- Where the JS browser API is documented for users (docs site vs `samples.go`), so the new syntax lands next to the existing one.
- That `DOM.setAttribute` / page JS tagging does not trip the egress guard (`session.go:476`).
