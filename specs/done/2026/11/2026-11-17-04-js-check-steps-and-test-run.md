---
model: sonnet
effort: medium
pr: pingdom-tms
automerge: false
---

# JS browser checks report a single verdict; add named steps and a test run with per-step results

## Problem
A failing multi-step JS browser check reports one error for the whole script. Pingdom (and the Chrome Recorder) show which step failed, how long each took, and what the page looked like. When writing a check, our user also has to save it and press "run now" (`server/internal/handlers/checkrunnow/handler.go:14-28`, runs a saved check in every region) to try it, with no step-by-step view.

## Proposal
1. **`step(name, fn)` global** in the JS engine (`server/internal/checkers/checkjs/checker.go`, next to `registerSecrets`, `checker.go:496-511`): runs `fn`, records `{name, status: ok|failed|skipped, durationMs, error?}`; on failure rethrows, and every later step is reported `skipped`. Nesting not supported (error). Code outside steps behaves as today.
2. **Per-step screenshot on failure**: when the browser is open and the check has `screenshot` enabled, the failing step gets the capture (reuse the existing time-boxed capture, `checkbrowser/config/config.go:30-40`); optional `step(name, fn, {screenshot: true})` captures on success too, bounded to N captures per run.
3. **Steps in the result**: stored with the result output and shown on the result detail page as a list (✅ / ❌ / ⏭, duration, error, thumbnail). The result's summary message becomes `step 4/7 "Click Se connecter" failed: …` instead of the raw script error.
4. **Test run of an unsaved check**: an endpoint that executes a check definition once, in one region, without storing it or alerting, and returns the result (with steps). Wire it to a "Test" button in the JS check editor that shows the step list inline.
5. Importers (`pingdom-tms-import`, `chrome-recorder-import`) wrap each translated step in `step(...)` with a human label (`Click "Se connecter"`).

## Tests
- `checkjs`: 3 steps, 2nd throws → statuses ok/failed/skipped, durations set, summary message names step 2; nested `step` → error; no `step` used → unchanged output (regression).
- Screenshot only on the failing step by default; cap respected.
- Test-run endpoint: runs without creating a check or a result row, no notification sent; viewer role refused like other write-floor check endpoints (`handlers/checks/handler.go:101-115`).
- Dashboard: Playwright test on the editor "Test" button showing a failed step (`data-testid` on the list and items).

## To verify
- Whether a test run of an unsaved check already exists (the dashboard bundle parses a streamed `{"type":"result"}` message, maybe from such an endpoint, or from AI authoring); reuse it if so.
- Where result output is stored and its size limit, so steps + thumbnails fit.
