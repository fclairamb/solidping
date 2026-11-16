---
model: sonnet
effort: medium
pr: pingdom-tms
automerge: false
---

# Import Chrome DevTools Recorder exports as JS browser checks

## Problem
Writing a multi-step browser check means writing JS by hand. Pingdom solves it with its own recorder; we don't need to build one: Chrome ships a recorder (DevTools → Recorder) that everyone already has, that replays flows locally for debugging, and that exports a documented JSON "user flow" (the `@puppeteer/replay` schema). Importing that JSON gives us a recorder for free.

## Proposal
1. **New import source `chrome-recorder`** in `server/internal/handlers/checks/importers/` (`converter.go:29-38`, `handler.go:87-98`): a pure `Converter` (no I/O), input = the exported JSON (`{"title": …, "steps": [...]}`), one recording → one `js` check named after `title`.
2. **Step mapping** to the generated script (`const page = browser.open();` then one statement per step, each wrapped in the step marker of spec `js-check-steps-and-test-run` when that lands, so results say which step failed):
   - `navigate` → `page.goto(url)`; `click` / `doubleClick` → `page.click(selectors)` (array of the step's `selectors`, flattened: each entry is itself an array whose LAST element is the target, earlier ones are frames/shadow hosts: take single-element entries as-is, multi-element entries → warning + skipped); ignore `offsetX/offsetY`, `button` other than primary → warning;
   - `change` → `page.fill(selectors, value)`;
   - `keyDown`+`keyUp` pairs → `page.press(<last focused selectors>, key)`; a lone `keyDown` / modifiers → warning;
   - `waitForElement` → `page.waitFor(selectors)`, plus `count` / `operator`, `visible: false`, `attributes`, `properties` turned into assertions via `page.evaluate`;
   - `waitForExpression` → poll `page.evaluate(expression)` until truthy (bounded by `timeout`);
   - `hover`, `scroll`, `setViewport`, `emulateNetworkConditions`, `close`: no-op with a comment (`setViewport`: warning only if very different from the 1280×800 capture viewport, `checkbrowser/config/config.go:34`);
   - `assertedEvents: [{type: "navigation", url}]` on any step → after it, assert `page.url()` matches.
   - `timeout` on a step → per-call timeout where the binding accepts `opts` (`checkjs/browser.go:192`).
3. **Unsupported → warning + `// TODO` line + check created paused** (same rule as the Pingdom import): steps with `frame` (iframes), `target` other than `main` (popups / new tabs; a check drives one page, `checkjs/browser.go:73`), unknown `type`, `customStep`.
4. **Passwords never in the script.** A `change` step whose selectors point at a password field (`aria/` name or CSS matching `password|passwd|pwd|mot de passe`, or `type=password` in any CSS entry) has its value moved to the check's encrypted `secrets` (`checkjs/config/config.go:56-66`) as `RECORDER_SECRET_<n>`, and the script reads `secrets.RECORDER_SECRET_<n>`. Always add a warning telling the user which values were moved, and that other typed values (emails, OTPs) stay in clear and should be reviewed.
5. **Entry points**: the convert endpoint (`POST …/checks/import/convert?source=chrome-recorder`), the dashboard import dialog (file upload or paste), and a short doc page "Record a flow with Chrome and import it" (screenshots of DevTools → Recorder → Export → JSON).

## Tests
- `chromerecorder_test.go` + golden files in `testdata/`: a real login recording (navigate, click, change ×2, keyDown/keyUp Enter, waitForElement, assertedEvents navigation) → expected script.
- Password field value → in `secrets`, absent from script text and from the convert response manifest; warning present.
- Recording with `frame` / `target: "popup"` / unknown step → warnings, TODO lines, check paused.
- Malformed JSON / no steps / no `navigate` first → clear conversion error.
- Generated script passes JS config validation (`checkjs/config/validate.go`).

## To verify
- The current `@puppeteer/replay` schema (step types and fields) against its published `Schema.ts`; Chrome has added step types over versions.
- How a converter attaches encrypted `secrets` to a generated check (shared with spec `pingdom-tms-import`).
