---
model: opus
effort: high
---

# The `js` and `browser` checks cannot be combined: a script can run a browser check whole, but never drive its page

## Problem

SolidPing ships two check types that, together, would cover the one workflow
every "synthetic monitoring" buyer asks for — log in through the real form,
confirm the app rendered, then hit an authenticated endpoint — and neither can
do it alone, nor can they be composed to do it.

**The `browser` check is a page-load smoke test.** Its whole configuration is
`url`, `waitSelector`, `keyword`, `invertKeyword`, `screenshot`, `timeout`
([config.go:15-27](server/internal/checkers/checkbrowser/config.go:15)), and
its whole execution is navigate → wait for `body` or the selector → read the
title → optionally match the keyword against the body text
([checker.go:436-465](server/internal/checkers/checkbrowser/checker.go:436)).
There is no click, no typing, no evaluation. Yet its own documentation lists
"Login flow verification" as a use case
([check-types.md:1204-1208](web/docs/docs/features/check-types.md:1204)).
That is an overclaim today: a browser check can confirm the login *page*
loads, not that logging in works.

**The `js` check has the logic but no page.** It runs on goja — synchronous,
no DOM — with `http.*`, `http.session()`, `env`/`secrets`, `console`, and the
`solidping.<type>()` sub-checks
([javascript-checks.md:146-297](web/docs/docs/features/javascript-checks.md:146)).
It can post a login form and hold a cookie jar, which covers server-rendered
apps. It cannot exercise a SPA whose login is JavaScript-driven, wait for a
client-side render, or read what the user would actually see.

**Composition exists in exactly one direction, at the wrong granularity.** A
script can already call `solidping.check("browser", { url, waitSelector,
keyword })` — `browser` is missing from the typed wrapper list at
[checker.go:331-335](server/internal/checkers/checkjs/checker.go:331), but
only `js` and `heartbeat` are refused
([checker.go:353-361](server/internal/checkers/checkjs/checker.go:353)), so the
generic form works. What comes back is a finished `{ status, metrics, output }`.
The script can *aggregate* a browser check; it cannot *steer* one. Nothing
lets it fill a field, click, or ask the page a question.

Why this matters beyond the feature gap:

- [positioning.md:30](wiki/competitors/positioning.md:30) names
  "JavaScript-scripted checks" as what SolidPing *wins on* against the
  end-to-end-testing buyer. That win is hollow while the scripted check cannot
  touch a browser.
- [market-feedback.md:276](wiki/research/market-feedback.md:276) explicitly
  rules out "Synthetic Browser Testing as the next big bet" — Checkly and
  Datadog own that lane and compete on price-per-run. So the answer is *not* a
  Playwright runtime (see Decisions), which is what makes the gap
  worth closing cheaply rather than expensively.
- Everything the feature needs already exists in the browser checker and is
  unreachable from the script: a per-worker concurrency cap
  ([checker.go:47-53](server/internal/checkers/checkbrowser/checker.go:47)),
  an isolated incognito context per execution
  ([checker.go:410-418](server/internal/checkers/checkbrowser/checker.go:410)),
  a CDP pre-flight that keeps "our sidecar is down" from reading as "the
  customer's site is down"
  ([checker.go:208-222](server/internal/checkers/checkbrowser/checker.go:208)),
  a screenshot capture with size and time caps that rides the existing
  attachment pipeline
  ([checker.go:258](server/internal/checkers/checkbrowser/checker.go:258),
  [diagnostics.go:35](server/internal/checkers/checkerdef/diagnostics.go:35)),
  and an availability cache that turns infrastructure failures into `error`,
  never `down`
  ([checker.go:183-200](server/internal/checkers/checkbrowser/checker.go:183)).

## Proposal

Add a `browser` global to the JS runtime: a **deliberately small, synchronous,
exhaustively documented** page-driving API backed by chromedp against the same
headless Chrome the `browser` check already uses. One page per execution, a
dozen methods, `evaluate` as the escape hatch. Not Playwright, and not trying
to become it.

### 1. The `browser` API

All calls are synchronous (goja is synchronous, chromedp's `Run` is
synchronous — there is no impedance to hide). Every page method returns a
plain result object and **never throws for a target-side failure**; only
infrastructure failures throw, which the runtime already reports as status
`error` ("the script is broken / the worker is broken", never "the target is
down"). This is the same split `http.*` makes with its `{ error }` return
([javascript-checks.md:231](web/docs/docs/features/javascript-checks.md:231)).

| Call | Returns | Notes |
|---|---|---|
| `browser.open()` | `page` | Acquires a browser slot, pre-flights the CDP endpoint, opens a fresh isolated context and tab. **Throws** when no Chrome is available, the type is disabled on this server, or a page is already open. Blocks while waiting for a slot — see §3. |
| `page.goto(url)` | `{ ok, url, title, duration, error? }` | Navigates and waits for `body` to be ready — exactly what the browser check does with no `waitSelector` ([checker.go:449-452](server/internal/checkers/checkbrowser/checker.go:449)). `url` must pass the same validation as `BrowserConfig.Validate` ([config.go:112-127](server/internal/checkers/checkbrowser/config.go:112)): `http`/`https` only, `file:`/`data:`/`javascript:` rejected. |
| `page.waitFor(selector, { timeout? })` | `{ ok, duration, error? }` | `chromedp.WaitVisible`. `timeout` is a duration string or ms, defaulting to the script's remaining time and never exceeding it — same rule as the `http.*` `timeout` option ([checker.go:513-519](server/internal/checkers/checkjs/checker.go:513)). |
| `page.click(selector)` | `{ ok, error? }` | `chromedp.Click`. |
| `page.fill(selector, text)` | `{ ok, error? }` | Clear, then send keys — **not** `SetValue`, so framework listeners (React/Vue controlled inputs) see real input events. |
| `page.press(selector, key)` | `{ ok, error? }` | Sends one key (`"Enter"`, `"Tab"`, …) to the element. Submitting a form with Enter is common enough to earn its one line. |
| `page.text(selector)` | `{ ok, text, error? }` | `chromedp.Text`; capped at the same 1 MB as an HTTP body ([checker.go:66](server/internal/checkers/checkjs/checker.go:66)). |
| `page.evaluate(expression)` | `{ ok, value, error? }` | Runs the expression **inside the page** — real DOM, page origin — and returns its JSON-serialisable value (a returned Promise is awaited, bounded by the script's remaining time). A throw inside the page is `{ ok: false, error }`. Same 1 MB cap. This is the escape hatch for everything the table does not have. |
| `page.url()` | `string` | Current top-frame URL — how a script asserts it landed on `/dashboard`. |
| `page.cookies()` | `[{ name, value, domain, path, secure, httpOnly, expires }]` | Via CDP `Network.getCookies`. Its purpose is handing a browser-established session to `http.*` — see the example in §7. |
| `page.screenshot()` | `{ ok, error? }` | Full-page PNG attached to the result's diagnostics — §5. |
| `page.close()` | `undefined` | Disposes the context and releases the slot. Called implicitly when the script returns, throws, or is interrupted; calling it twice is a no-op. |

Not in the table, on purpose: multiple pages or tabs, frames, downloads,
uploads, request interception, header/UA overrides, viewport emulation, video,
tracing. `evaluate` covers the long tail; the doc says so in as many words, the
way the existing page already refuses `fetch`/`Promise`/`require`
([javascript-checks.md:30-34](web/docs/docs/features/javascript-checks.md:30)).

### 2. Implementation: one code path, shared with the browser check

`checkbrowser` currently keeps everything a session needs unexported and
interleaved with the check's own verdict logic. Extract a session type and
rebuild the browser check on it, so there is **one** way to open, drive, and
tear down a Chrome in this codebase:

```go
package checkbrowser

// Open acquires a slot, pre-flights the CDP endpoint (remote path), builds the
// allocator and an isolated context, and allocates the browser EAGERLY with an
// empty chromedp.Run. Every error it returns is infrastructure by construction.
func Open(ctx context.Context) (*Session, error)

func (s *Session) Navigate(ctx context.Context, url string) (NavResult, error)
func (s *Session) WaitVisible(ctx context.Context, sel string) error
func (s *Session) Click(ctx context.Context, sel string) error
func (s *Session) Fill(ctx context.Context, sel, text string) error
func (s *Session) Press(ctx context.Context, sel, key string) error
func (s *Session) Text(ctx context.Context, sel string) (string, error)
func (s *Session) Evaluate(ctx context.Context, expr string) (any, error)
func (s *Session) URL(ctx context.Context) (string, error)
func (s *Session) Cookies(ctx context.Context) ([]Cookie, error)
func (s *Session) Screenshot(ctx context.Context) ([]byte, error)
func (s *Session) Close()

// Infra reports whether err means the browser itself is gone (CDP socket
// closed, target crashed) as opposed to a target-side failure such as a
// selector that never appeared.
func Infra(err error) bool
```

- `Open` is `acquireSlot` + `probeCDP` + `allocator` + `browserContext`
  ([checker.go:77-84, 208-222, 393-418](server/internal/checkers/checkbrowser/checker.go:77))
  plus an eager `chromedp.Run(ctx)`. The eager allocate is what makes the
  `browserWasAllocated` hazard ([checker.go:343-365](server/internal/checkers/checkbrowser/checker.go:343))
  a non-issue for page methods: a `Session` that exists has a browser attached,
  and one whose allocation failed is disposed inside `Open` and never `Run`
  again. Keep the guard anyway, as the cheap defence it is.
- `Open` records the outcome the way `recordOutcome` does
  ([checker.go:183-200](server/internal/checkers/checkbrowser/checker.go:183)):
  an infra failure calls `MarkUnavailable()` immediately, a successful open
  calls `MarkAvailable()`.
- The browser check's `runBrowser`/`navigateAndCheck`/`checkKeyword`/
  `captureScreenshot` become a thin verdict layer over `Session`. Its existing
  test seams (`session` and `screenshot` func fields,
  [checker.go:62-71](server/internal/checkers/checkbrowser/checker.go:62)) are
  re-pointed at the new type; `checker_test.go`, `availability_test.go` and
  `screenshot_test.go` must stay green in *meaning*, not just compile.
- `checkjs` imports `checkbrowser` directly. There is no cycle:
  `checkbrowser` imports only `chromedp` and `checkerdef`
  ([checker.go:1-32](server/internal/checkers/checkbrowser/checker.go:1)). The
  `ResolveChecker` indirection ([checker.go:61](server/internal/checkers/checkjs/checker.go:61))
  stays for the registry-level sub-checks; it is not needed here.
- `checkjs` gets a package-level `OpenBrowser = checkbrowser.Open` seam so its
  goja-binding tests run against a fake `Session` with no Chrome — the same
  pattern as `checkbrowser`'s `fakeCDPServer` + `session` seam
  ([checker_test.go:35,116](server/internal/checkers/checkbrowser/checker_test.go:35)).
  CI's backend job has no Chrome (only the E2E jobs install Playwright's
  Chromium, [ci.yml:401](.github/workflows/ci.yml:401)), so this is not
  optional.

### 3. Lifecycle, timeouts, cancellation

- **One page per execution.** A second `browser.open()` throws. The session
  is opened on a context derived from the execution context and closed by a
  `defer` in `Execute` ([checker.go:113-143](server/internal/checkers/checkjs/checker.go:113)),
  so a script that returns early, throws, or is interrupted never leaks a
  context or a slot.
- **The slot is held from `open()` to `close()`**, not per action. That is the
  honest cost model: a script holding a page is a browser check in flight, and
  the semaphore ([checker.go:53](server/internal/checkers/checkbrowser/checker.go:53))
  is what protects the sidecar. `open()` waits for a slot on the execution
  context, so on a saturated worker the script simply times out, and
  `output.error` names the slot wait exactly as the browser check does
  ([checker.go:157-159](server/internal/checkers/checkbrowser/checker.go:157)).
- **Every page method runs on a context derived from the execution
  context.** This is the load-bearing rule. goja's `Interrupt` (fired from the
  goroutine at [checker.go:131-135](server/internal/checkers/checkjs/checker.go:131))
  is only observed when control returns to JavaScript; a Go binding blocked in
  `chromedp.Run` would otherwise hold the script past its timeout. With the
  derived context the blocked `Run` returns on cancel, the binding returns,
  and the interrupt lands — the result is `timeout`, the deferred close
  disposes the page. A test must prove a `waitFor` on a selector that never
  appears, with no per-call `timeout`, ends the script at the check's timeout
  and not later.
- **The browser check's post-timeout screenshot trick** (`sessionCtx`
  outliving `probeCtx`, [checker.go:196-201](server/internal/checkers/checkbrowser/checker.go:196))
  is not reproduced for scripts in this spec: after an interrupt there is no
  script left to call `page.screenshot()`. A script that wants a picture of a
  failure takes it before returning `down`. Auto-capture on interrupt is a
  follow-up (Out of scope).

### 4. Budgets and caps

| Limit | Value | Where |
|---|---|---|
| Pages per execution | 1 | `browser.open()` |
| Browser actions per execution | 100 | every `page.*` call except `url()`/`close()`; the 101st returns `{ ok: false, error }` naming the limit — the same shape as the sub-check limit ([checker.go:365-371](server/internal/checkers/checkjs/checker.go:365)) |
| `text` / `evaluate` payload | 1 MB | shared constant with `maxHTTPBody` |
| Screenshot | `MaxScreenshotBytes`, `screenshotTimeout` | unchanged from the browser check |
| Script timeout | ≤ 30 s, unchanged | [config.go:14-15](server/internal/checkers/checkjs/config.go:14) |

Browser actions are **not** counted against the 20-call `http.*`/`solidping.*`
budget. They are a different resource (one held slot, many cheap CDP
round-trips) and folding them in would let one login flow exhaust the budget
a script needs for the API call it was logging in *for*.

### 5. Screenshot semantics

`page.screenshot()` captures into `result.Diagnostics.Screenshot`
([diagnostics.go:35](server/internal/checkers/checkerdef/diagnostics.go:35))
through the same `fullScreenshot` + size/time caps the browser check uses.
Rules, all inherited rather than invented:

- **Last successful call wins.** One capture per result, so a script can
  overwrite an early "before" shot with the one taken at the moment it decides
  the target is down.
- **Only a failing result keeps it.** The browser check captures only for
  `capturableStatus` verdicts; here the script decides *when* to shoot, but
  the runtime drops the capture after the script returns unless the final
  status is one the browser check would have kept. A screenshot on an `up`
  run costs a CDP round-trip and nothing else. This keeps storage, retention,
  the agent's out-of-band upload ([diagnostics.go:58-66](server/internal/checkers/checkerdef/diagnostics.go:58))
  and the incident card ([incidents.$incidentUid.tsx:1433](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1433))
  entirely unchanged — no new dash0 work.
- The caption's honesty rule ("what the page looked like *after* the verdict",
  [diagnostics.go:50-56](server/internal/checkers/checkerdef/diagnostics.go:50))
  holds even better here: the script took the picture, so the doc says the
  timestamp is when *it* asked.

### 6. Availability, the type gate, and placement

- **No Chrome on this worker → `error`, never `down`.** `browser.open()`
  throws with the browser check's own message
  ([checker.go:555-563](server/internal/checkers/checkbrowser/checker.go:555)),
  the runtime reports status `error` with it in `output.error`, and the
  availability cache is marked unavailable. A remote CDP endpoint that stops
  answering mid-script is an infra error too (`checkbrowser.Infra`) and throws
  from whichever page method hits it; a selector that never appears is not,
  and returns `{ ok: false }`.
- **The server-level type gate covers it.** `browser.open()` consults
  `TypeEnabled(checkerdef.CheckTypeBrowser)` ([checker.go:52-61](server/internal/checkers/checkjs/checker.go:52),
  installed at [worker.go:342](server/internal/checkworker/worker.go:342)) and
  throws "check type \"browser\" is disabled on this server" — the exact
  string sub-checks use. An operator who turned `browser` off does not get it
  back through a script. The per-org gate is *not* org-aware here, for the
  reason already written at [checker.go:383-388](server/internal/checkers/checkjs/checker.go:383);
  that follow-up applies to both paths and is not widened by this spec.
- **Placement is unchanged, and the doc says so.** A `js` check is scheduled
  as a `js` check (`requires:scripting-runtime`,
  [types.go:336](server/internal/checkers/checkerdef/types.go:336)); the
  `browser` capability workers advertise ([worker.go:80](server/internal/db/models/worker.go:80),
  [egressreport.go:79-84](server/internal/checkworker/egressreport/egressreport.go:79))
  routes `browser`-type checks only. A script that opens a browser on a region
  without one gets the `error` above. The doc's guidance: give every region
  that runs `js` checks a headless-shell sidecar ([docker-compose.yml:24](docker-compose.yml:24)
  already does), or pin such checks to regions whose capability list shows
  `browser`. Routing scripts by inferred requirement is Out of scope.

### 7. Period floor

`js` has a 30 s floor, `browser` a 60 s one ([types.go:336,348](server/internal/checkers/checkerdef/types.go:336)),
and the browser floor exists because a headless run costs seconds and would
otherwise occupy a slot continuously ([check-types.md:1200](web/docs/docs/features/check-types.md:1200)).
A 30 s script holding a page for most of that is the exact load the floor
prevents, and on a 4-slot worker one such check starves every browser check
next to it into "timed out waiting for a free browser slot".

Decision: **a script that uses the browser inherits the browser floor, decided
at validation time.**

- Add an optional interface in `checkerdef`:
  `type MinPeriodHint interface { MinPeriodHint() time.Duration }`.
  `JSConfig` implements it, returning the `browser` meta's `MinPeriod` when
  the script matches `\bbrowser\s*\.\s*open\s*\(` and `0` otherwise.
- `validatePeriodForType` ([service.go:313](server/internal/handlers/checks/service.go:313))
  takes the parsed config alongside the type and uses
  `max(meta.MinPeriod, config.MinPeriodHint())` as the floor. The
  `VALIDATION_ERROR` names the reason: "scripts that open a browser have the
  browser check's 1m floor".
- The create-without-period path fixed by spec `2026-09-11-07` needs no
  change: `js`'s `DefaultPeriod` is already 1m, equal to the browser floor. A
  test pins that.
- This is a heuristic and the spec says so. A false negative (`var b =
  browser; b.open()`) just runs at 30 s under the semaphore's protection; a
  false positive (the call in a comment) is a visible validation error the
  user can read. Both are acceptable; a runtime rule cannot do better because
  `Execute` never sees the period.

### 8. Documentation and samples

- **`javascript-checks.md`**: a new `### browser` subsection in the API
  reference between `http.session()` and `solidping.*`
  ([lines 248-274](web/docs/docs/features/javascript-checks.md:248)), one
  table row per method in §1 including the return shape, the throw-vs-return
  rule, the one-page rule, and the "no Chrome → `error`" behaviour. Rows in
  the Limits table ([line 299](web/docs/docs/features/javascript-checks.md:299)).
  A full example **"Form login through a real browser"** under Full examples
  ([line 311](web/docs/docs/features/javascript-checks.md:311)):

  ```js
  var page = browser.open();
  var nav = page.goto(env.BASE_URL + "/login");
  if (!nav.ok) return { status: "down", output: { step: "load", error: nav.error } };
  page.fill("#email", env.USERNAME);
  page.fill("#password", secrets.PASSWORD);
  page.click("button[type=submit]");
  var dash = page.waitFor("[data-testid=dashboard]", { timeout: "10s" });
  if (!dash.ok) {
    page.screenshot();
    return { status: "down", output: { step: "login", url: page.url(), error: dash.error } };
  }
  // Hand the browser's session to a plain HTTP call: cookies go on the header,
  // http.session()'s jar is filled by the target only (httpsession.go:11-17).
  var cookie = page.cookies().map(function (c) { return c.name + "=" + c.value; }).join("; ");
  var me = http.get(env.BASE_URL + "/api/me", { headers: { Cookie: cookie } });
  if (me.error || me.statusCode !== 200) {
    return { status: "down", output: { step: "api", statusCode: me.statusCode, error: me.error } };
  }
  return { status: "up", metrics: { loginMs: nav.duration + dash.duration } };
  ```

  Troubleshooting entries ([line 523](web/docs/docs/features/javascript-checks.md:523)):
  "browser is disabled on this server", "no Chrome on this worker", "a page
  is already open", "browser action limit of 100 exceeded", and the period
  floor error.
- **`check-types.md`**: the browser section's use-case list
  ([lines 1204-1208](web/docs/docs/features/check-types.md:1204)) loses
  "Login flow verification" and gains one sentence: *to drive the page —
  fill a form, click, assert on the result — write a `js` check and use its
  `browser` API*, linked. The JS section ([lines 1175-1187](web/docs/docs/features/check-types.md:1175))
  gains the matching use-case bullet. The browser section's "Where Chrome
  comes from" gets the one line from §6: the sidecar now serves `js` checks
  too.
- **Samples**: a fourth entry in `GetSampleConfigs`
  ([samples.go:29](server/internal/checkers/checkjs/samples.go:29)),
  `js-browser-login`, carrying the *same* script as the doc example, under the
  same drift guard the two existing promoted samples use
  ([samples.go:16-24](server/internal/checkers/checkjs/samples.go:16)). The
  dashboard's sample picker lists it with no frontend change. The JS form's
  help text ([checks.json:947](web/dash0/src/locales/en/checks.json:947))
  mentions `browser` in one clause, in every locale.

### 9. Tests

- **Binding tests without Chrome** (`checkjs`, fake `Session` through the
  `OpenBrowser` seam): every method's return shape; target-side failure
  returns `{ ok: false }` and the script can still return `down`; infra
  failure throws and the result is `error` with the message in
  `output.error`; second `open()` throws; the 100-action cap; the browser
  budget does not consume the 20-call budget and vice versa; `open()` refused
  when `TypeEnabled` says `browser` is off; `goto` rejects `file:`/`data:`/
  `javascript:` URLs; screenshot kept on `down`, dropped on `up`, last call
  wins; `close()` idempotent; the deferred close runs on throw and on
  interrupt.
- **The cancellation proof** from §3: a fake `Session` whose `WaitVisible`
  blocks until its context is cancelled; the script must end at the check's
  timeout with status `timeout`, and the session must be closed afterwards.
- **Availability**: an infra failure from `Open` marks the cache unavailable;
  a successful open marks it available (extend `availability_test.go`).
- **Browser check refactor**: the existing `checkbrowser` suites pass on the
  `Session`-based implementation with their assertions intact — verdicts,
  the "SP_CHECKERS_BROWSER_CDP_URL" infra message
  ([checker_test.go:85](server/internal/checkers/checkbrowser/checker_test.go:85)),
  screenshot caps, slot timeout.
- **Period floor** (`handlers/checks`): create/patch of a `js` check whose
  script opens a browser is rejected under 1m with the named reason; accepted
  at 1m; a script without `browser.open(` keeps the 30 s floor; no period +
  browser script stores 1m.
- **Docs**: the new example is compile-checked by `docs_examples_test.go`
  like every other fence. It is additionally **executed** end to end — login
  form served by the `httptest` fixture, real Chrome — when a CDP endpoint is
  reachable (`SP_CHECKERS_BROWSER_CDP_URL` set, or a local Chrome found),
  and skipped with a visible reason otherwise. The sample and the doc example
  share the script, so the drift guard covers both.

### Decisions

- **Why not Playwright.** Real Playwright means a Node runtime in every
  worker and deported agent (all Go today, image distroless by design,
  [check-types.md:1212](web/docs/docs/features/check-types.md:1212)) *and* a
  new sandbox story — goja is the sandbox now; Node is not, so it would need
  isolated-vm or a container per run. That is exactly the expensive part of
  Checkly and the reason its browser checks are priced per run, which the
  market research says not to compete on. It would also put two JavaScript
  dialects in one product (sync goja, async Node). `playwright-go` does not
  escape this: it spawns a Node driver anyway, and bridging it into goja
  means designing an API of our own regardless. The buyer it would serve —
  "paste my e2e suite into the monitor" — is the lane the positioning wiki
  already concedes.
- **Why not a script field on the browser check.** The moment a browser check
  carries a script it wants secrets, `http`, `console`, sub-checks and the
  result contract — i.e. the JS runtime. Building that a second time inside
  `checkbrowser` is the wrong home. The browser check stays the no-code
  smoke test it is.
- **Return, don't throw, for target failures.** The runtime's contract
  separates `down` (target failed) from `error` (script/worker broken)
  ([javascript-checks.md:124-131](web/docs/docs/features/javascript-checks.md:124)).
  Playwright throws on everything; copying that would turn "the login button
  never rendered" into an `error`, which does not page. Infra failures throw
  because they *should* be `error`.
- **Synchronous by construction.** No `await`, no callbacks, no promise
  polyfill. It matches every other global on the page and what goja and
  chromedp already are.
- **`fill` sends keys, `SetValue` is not exposed.** Setting `.value` directly
  bypasses the input events controlled inputs listen for; a login form on a
  React app would silently submit empty fields.

### Open questions

- **HTTP status of the navigation.** `chromedp.Navigate` does not return the
  main-frame response status; exposing it means listening for
  `Network.responseReceived`. Default for this spec: `goto` returns no status
  code; a script that needs one reads it from `page.evaluate` or hits the URL
  with `http.get`. Revisit if asked.
- **Payload cap for `evaluate`.** 1 MB mirrors `http` bodies; 64 KB would
  match the script size and be plenty for assertions. Default: 1 MB, one
  shared constant, so there is one number to change.

### Out of scope

- A Playwright/Node runtime, `@playwright/test` compatibility, a recorder UI.
- Multiple pages/tabs, frames, downloads, uploads, request interception,
  header/UA/viewport overrides, video, tracing, HAR.
- Auto-screenshot on script timeout (the browser check's `sessionCtx` trick).
- `http.session()` cookie injection (`session.setCookies`); cookies travel on
  a `Cookie` header, see the example.
- Routing `js` checks that use the browser to browser-capable regions; the
  regexp floor in §7 is the only static inference this spec adds.
- The org-aware sub-check gate already noted at
  [checker.go:383-388](server/internal/checkers/checkjs/checker.go:383).
- Script-editor autocomplete for the `browser` API in dash0.

## Implementation Plan

1. **`checkbrowser.Session` (§2).** New `session.go`: `Open(ctx)` = `openSession(ctx, ctx)`,
   where `openSession(sessionCtx, probeCtx)` does `acquireSlot` → `probeCDP` (remote) →
   `allocator` → `browserContext` → an EAGER `chromedp.Run(ctx)`, mapping every failure to
   the browser check's existing infra messages and recording the outcome on the availability
   cache (`MarkAvailable` / `MarkUnavailable`; a slot timeout records nothing). Page methods
   (`Navigate`/`WaitVisible`/`Click`/`Fill`/`Press`/`Text`/`Evaluate`/`URL`/`Cookies`/
   `Screenshot`/`Close`) run every `chromedp.Run` on a child of the session's browser context
   that `context.AfterFunc` cancels when the CALLER's context ends — the §3 rule.
   `Infra(err)` classifies. `ErrSlotTimeout` is the sentinel the slot wait returns.
2. **Browser check rebuilt on it.** `Execute` no longer acquires the slot (`openSession`
   does); `runBrowser` opens a `Session` and `navigateAndCheck`/`checkKeyword`/
   `captureScreenshot` become a verdict layer over it. The `session` and `screenshot` test
   seams keep their signatures — `session` is spliced in behind the same CDP pre-flight it
   always was, so `checker_test.go`, `availability_test.go` and `screenshot_test.go` keep
   their assertions verbatim.
3. **`browser` global (§1, §3, §4, §6).** New `checkjs/browser.go`: the `BrowserSession`
   interface, the `OpenBrowser` seam (`= checkbrowser.Open`), `browser.open()` with the
   one-page rule, the `TypeEnabled(CheckTypeBrowser)` gate and the infra-throws /
   target-returns split, plus the 100-action budget kept separate from the 20-call
   `http.*`/`solidping.*` budget.
4. **Screenshot semantics (§5).** `page.screenshot()` stores the last successful PNG on the
   runtime; `Execute` attaches it to `result.Diagnostics` only when the final status passes
   `checkbrowser.CapturableStatus`. Timeout mapping: an interrupt caused by the check's own
   deadline now reports `timeout` rather than `error`, which is what the doc already promises
   and what §3's proof requires.
5. **Period floor (§7).** `checkerdef.MinPeriodHint`, implemented by `JSConfig` (regexp
   `\bbrowser\s*\.\s*open\s*\(`); `validatePeriodForType` takes the parsed config and floors
   at `max(meta.MinPeriod, hint)`, with the reason named in the message.
6. **Docs, samples, locales (§8)** and **tests (§9)**, written alongside each step.
