---
model: sonnet
effort: medium
pr: pingdom-tms
automerge: false
---

# Import Pingdom transaction (TMS) checks as JS browser checks

## Problem
Pingdom's transaction checks are its main paid differentiator, and the hardest thing to leave behind when migrating: customers have to re-create every flow by hand. We already import from Gatus, Better Stack, Uptime Kuma and UptimeRobot (`server/internal/handlers/checks/importers/`), but not from Pingdom.

A TMS check is not code: it is a JSON list of steps, each a fixed command with arguments (Pingdom API 3.1, "TMS Steps Vocabulary"):

```json
"steps": [
  {"fn": "go_to", "args": {"url": "pingdom.com"}},
  {"fn": "click", "args": {"element": "START FREE TRIAL"}},
  {"fn": "url",   "args": {"url": "https://www.pingdom.com/sign-up/"}}
]
```

So it translates mechanically into a JS check script, given the selector prefixes, the `check/select/submit` methods and `page.basicAuth` from the two earlier specs of this PR.

## Proposal
1. **New source `pingdom`** in `importers/converter.go:29-38` and `handler.go:87-98`, implemented as a `ContextConverter` like Better Stack (`betterstack.go`): request body `{"token": "<Pingdom API token>"}`, the token lives only in the request.
   - `GET https://api.pingdom.com/api/3.1/tms/check` (paginate with `limit`/`offset`), then `GET /tms/check/{cid}` for each, Bearer auth. Overridable base URL for tests, like `WithBetterStackBaseURL` (`handler.go:72`).
   - Optionally also import the classic uptime checks (`/checks`) in a later spec: out of scope here.
2. **Each TMS check becomes one `js` check** whose `script` is generated: `const page = browser.open();` then one line per step, every Pingdom selector wrapped as `auto=…` (Pingdom does not say which kind it is). Map:
   - actions: `go_to`→`page.goto(url)` (prefix `https://` when scheme-less, as in the doc example), `click`→`page.click`, `fill`→`page.fill`, `check`/`uncheck`→`page.check`/`page.uncheck`, `select`→`page.select`, `select_radio`→`page.check("auto=" + name)` (verify a radio by `name=` resolves one element; else `css=input[type=radio][name="…"]` + warning), `submit`→`page.submit`, `sleep`→bounded wait, `wait_for_element`→`page.waitFor`, `wait_for_contains`→poll `page.text` until it contains, `basic_auth`→`page.basicAuth(secrets.PINGDOM_BASIC_USER, secrets.PINGDOM_BASIC_PASSWORD)` emitted **before** the `go_to`, with the values put in the check's encrypted `secrets`, never in the script text;
   - validations (each throws a clear message on failure): `url`, `exists`, `not_exists`, `contains`, `not_contains`, `field_contains`, `field_not_contains`, `is_checked`, `is_not_checked`, `radio_selected`, `dropdown_selected`, `dropdown_not_selected`, via `page.url()`, `page.text()` and `page.evaluate()`.
   - Name, interval (`interval` minutes → period), `active` → enabled, tags carried over; regions mapped best effort with a warning when not.
3. **"recording" checks** (`wpm_*` steps from the WPM recorder) are imported best effort: `wpm_go_to`, `wpm_click` (ignore offsets), `wpm_fill`, `wpm_select_dropdown`, `wpm_wait`, `wpm_contains_timeout` / `wpm_not_contains_timeout` (regex honoured); `wpm_click_xy`, `wpm_move_mouse_to_element`, `wpm_close_tab` produce a **warning** and a `// TODO` comment line in the script, and the check is created **paused** so it does not alert on an untranslatable flow.
4. Unknown `fn` → warning + `// TODO` line + check created paused. Warnings use the existing `warnings` helper (`converter.go:105-120`).
5. Add the mapping table to `importers/mapping.md`, and `pingdom` to the import UI source list.

## Tests
- `pingdom_test.go` with an `httptest` server: list + detail pagination, Bearer header sent, 401 → clear error, missing token → error like `ErrBetterStackTokenRequired`.
- Golden test (`golden_test.go` + `testdata/`): a "script" check using every command and validation → expected JS; a "recording" check with `wpm_click_xy` → warning, TODO line, paused.
- `basic_auth` step → credentials in `secrets`, **absent** from the script text and from the convert response's manifest.
- Generated script passes the JS check's config validation (`checkjs/config/validate.go`).

## To verify
- The exact JSON shape of `GET /tms/check/{cid}` (fields `steps`, `type`, `interval`, `region`, `active`, `tags`, `metadata`): the vocabulary is from the Pingdom 3.1 OpenAPI (mirrored in github.com/karlderkaefer/pingdom-golang-client `pkg/pingdom/openapi/api/openapi.yaml`); confirm field names against it before coding.
- How a converter attaches encrypted `secrets` to a generated check (Better Stack does not need it).
