# Browser monitoring

A "browser" check loads a real headless Chrome page, waits for content
to render, and asserts on the result. Use it when an HTTP check isn't
enough — single-page apps where the meaningful content arrives via
fetch/XHR after the initial response, server-rendered pages that
inject critical content via JavaScript, or scenarios where you care
about the rendered DOM rather than the response body.

## When to pick browser over http

| Symptom | Right check type |
|---|---|
| Server returns 200 + raw HTML, content is in the response body | `http` (with optional `keyword`) |
| Server returns 200 + a tiny shell, content is fetched and rendered client-side | `browser` |
| Server returns redirect chain that ends at a SPA shell | `browser` |
| You only care about response time and status code | `http` |
| You care about a specific element rendering correctly | `browser` |
| You're load-testing the page's render budget | neither — use a dedicated tool |

A browser check is **5-10× more expensive** than an HTTP check (a full
Chrome process spins up per execution). Don't use it for protocol
liveness; HTTP is what you want there.

## Configuration

The full schema is documented in
[checker-config.md](../conventions/checker-config.md#browser----headless-chrome-monitoring);
this page covers behavior and operational concerns.

```json
{
  "type": "browser",
  "name": "Dashboard renders OK",
  "config": {
    "url": "https://app.example.com/dashboard",
    "waitSelector": "[data-loaded]",
    "keyword": "Welcome",
    "invertKeyword": false,
    "timeout": "30s",
    "screenshot": false
  }
}
```

| Field | Effect |
|---|---|
| `url` | Required. Must be `http://` or `https://`. `file://`, `data:`, `javascript:` are explicitly rejected at validation time ([`config.go:100`](../../server/internal/checkers/checkbrowser/config.go)). |
| `waitSelector` | Optional CSS selector. The check waits for it to be **visible** (not just in the DOM) before continuing. If absent, the check waits for `body` to be ready. |
| `keyword` | Optional substring to look for in the rendered page text (`document.body.textContent` after JS has run). |
| `invertKeyword` | Default `false` — keyword must be present for UP. Set `true` to fail-on-found ("expect this error message to be gone"). |
| `timeout` | Default 30s. Hard cap 120s. The whole pipeline (alloc + navigate + waitFor + read) shares the budget. |
| `screenshot` | Default `false`. See [Failure screenshots](#failure-screenshots) below. |

## Failure screenshots

With `screenshot: true`, a **failing** execution captures a full-page PNG before
the browser context is disposed, and the incident pipeline keeps it — but only
for the run that OPENED or REOPENED an incident. Every other failing run's
capture exists in the worker's memory and is dropped: a flapping 30 s check
would otherwise mint 2,880 blobs a day for evidence nobody looks at.

What the capture is, precisely: what the page looked like a moment **after** the
check decided the target was unhealthy — not the failing frame. The incident
page labels it that way, and so should you when reading one; a page that
finished loading half a second later can look perfectly healthy.

Guardrails, all of them one-directional (a capture can be lost, never gained at
the expense of a verdict):

- The capture runs **after** the verdict is decided and can never change it. It
  is time-boxed, and detached from the check's own (possibly already-expired)
  deadline so a timed-out check still gets a picture.
- `StatusError` is never captured — from this checker it means "no browser to
  drive", so there is no page.
- Over `MaxScreenshotBytes` (4 MiB) the capture is **dropped**, never truncated.
  Half a PNG is a broken-image icon, not evidence.
- Storage: the blob is a `files` row attached to the incident via
  `files.topic = incidents/<uid>/screenshot` (spec 2026-08-21-01). It is served
  through a short-lived **signed** URL on the incident detail API, is reaped
  when the incident goes away, and is **never** exposed on a status page or in
  a subscriber payload.
- On reopen the relapse's capture replaces the previous one; a relapse with no
  capture drops the stale one rather than showing the old outage's picture next
  to a different failure.

### Deported agents

A private agent runs the same capture code, but its WebSocket to the master is a
JSON control channel, so the PNG cannot ride the result frame. The bytes are
split from the announcement (spec 2026-08-21-05):

1. The agent keeps the PNG in a small **bounded, TTL'd, take-once cache** (a few
   entries, a few MiB; `internal/agents/capturecache`) and puts only a marker on
   the result frame — `screenshot: {available: true, captureId: "…"}`. The bytes
   never touch the socket in any encoding.
2. If that result **opens or reopens an incident** — and only then — the server
   sends an `upload-request` frame back down the same socket, naming the capture
   id and a **server-generated** topic (`incidents/<uid>/screenshot`). Nothing in
   the topic comes from the agent.
3. The agent POSTs the bytes to `POST /api/v1/agent/attachments?topic=…` with
   its normal Ed25519 signed headers, and the attachment appears on the incident.

The ask is bounded by the incident state machine, not by a counter: a chatty
agent that marks every failing result still provokes at most one request per
open and one per reopen.

Every hop is best-effort and **one-shot**. If the agent has disconnected, is on
another replica, or has already evicted the capture, the screenshot is simply
lost — the incident opens exactly as it would have, and nothing is retried. In-
cluster and in-process browser checks skip all of this and hand the bytes over
directly.

## Execution model

Each check execution spins up its own ephemeral Chrome
([`checker.go:80`](../../server/internal/checkers/checkbrowser/checker.go)):

1. `chromedp.NewExecAllocator` creates a temporary user-data dir.
2. `chromedp.NewContext` starts the Chrome process under that allocator.
3. Navigate → wait for selector (or `body` ready) → read title.
4. If `keyword` is set: `chromedp.Text("body", …)` reads the rendered
   text and substring-matches.
5. The Chrome process is terminated and the user-data dir cleaned up
   when the function returns (via `defer`s).

**No shared browser pool.** Each check execution is an isolated Chrome.
Sessions, cookies, localStorage, service workers — none persist between
runs. This is intentional: the alternative (a long-lived Chrome) leaks
state across tenants and across runs of the same check.

The cost of cold-start Chrome is the price of that isolation. On a
modest worker node, expect ~500ms-1s of overhead per check on top of
the actual page load.

## What the result records

A successful browser check populates:

- **Metrics**:
  - `load_time_ms` — time from navigate to wait-for-element completion
  - `total_time_ms` — full pipeline time
- **Output**:
  - `title` — the page's `<title>` after render

Failure modes:

- **Navigation timeout**: Chrome could not reach the URL or it didn't
  finish within `timeout`. Status: `timeout`.
- **Wait selector never appeared**: the page loaded but the selector
  was never visible. Status: `down`.
- **Keyword present and `invertKeyword=true`** (or absent and
  `invertKeyword=false`): keyword assertion failed. Status: `down`.
- **Body read failed** (rare; usually a Chrome crash): Status: `down`,
  output carries the error string.

## Capabilities and limitations

What browser checks **can** do:

- Render JavaScript-heavy SPAs (React, Vue, Angular, Svelte, etc.).
- Wait for content that arrives via XHR/fetch/SSE after initial load.
- Assert on the rendered text — "is this error message visible?",
  "did the welcome banner load?".
- Work behind public DNS without needing your service to know about
  SolidPing.

What browser checks **cannot** do (today):

- **Auth flows.** No cookies / session preservation between runs. If
  you need to monitor a logged-in flow, the check has to log in
  every execution (which usually means embedding credentials in the
  config — workable, but heavier than a token-based heartbeat).
- **Form submission / multi-step flows.** The runner does navigate +
  wait + assert. No `chromedp.Click`, no input filling. If you need
  this, drop into a JavaScript check (`type: javascript`) and write
  the chromedp script directly.
- **Screenshots.** Not currently captured or stored. The dashboard
  tile shows the title and the keyword match outcome only.
- **Network capture / HAR**. The check times the navigation but
  doesn't record per-request waterfall data.
- **Custom user agent / headers**. Defaults to chromedp's default UA.
- **Authentication-required URLs.** Same problem as auth flows;
  basic-auth in the URL works for plain-HTTP, but everything stronger
  needs a JS check.

## Operational concerns

### Worker requirements

Browser checks **only run on workers with Chrome/Chromium installed**.
A worker without Chrome will fail every browser check with a
`failed to start chrome` error. The official worker container ships
with Chromium pre-installed; custom worker images need to add it.

There is **no automatic capability gate** — workers don't advertise
"can run browser checks" in their context, so a misrouted check will
just fail loudly. Use `context_conditions` on the check_job to pin
browser checks to known-capable workers; the convention `{"caps":
["browser"]}` set on those workers' context is the recommended pattern.

### Memory & process budget

Each check execution uses ~150-300MB of RSS for the Chrome process
(varies wildly with the page being loaded). A worker running 10
concurrent browser checks should plan for ~3GB above its baseline
footprint.

If your worker host is memory-constrained, reduce the worker's
concurrency limit rather than tweaking Chrome flags. The runner's
defaults are conservative; the squeeze is usually concurrency, not
per-check tuning.

### Regional deployment

Browser checks honor the same region selector as other checks. A
browser check pinned to `us-east` runs on a worker in that region,
giving you "is the rendered page reachable from us-east?" — useful
for geo-routed CDN monitoring.

### Security: schemes and SSRF

Validation rejects `file://`, `data:`, and `javascript:` URLs at check
create time and at execution time. Other schemes that Chrome
understands (`chrome://`, `about:`, etc.) will be passed through to
Chrome which will most likely refuse them; we don't proactively
restrict beyond the explicit deny-list.

The check runs whatever URL you give it from the worker's network
position. If a worker has access to an internal IP range, a browser
check pointed at an internal URL will reach it. Treat browser-check
URLs as "anything this worker can reach" — same trust model as HTTP
checks.

## Where to look in the code

| Concern | File |
|---|---|
| Config schema, validation | [`server/internal/checkers/checkbrowser/config.go`](../../server/internal/checkers/checkbrowser/config.go) |
| Execution (Chrome lifecycle, navigate, wait, assert) | [`server/internal/checkers/checkbrowser/checker.go`](../../server/internal/checkers/checkbrowser/checker.go) |
| Sample configs (used by the create-check picker) | [`server/internal/checkers/checkbrowser/samples.go`](../../server/internal/checkers/checkbrowser/samples.go) |
| Conventions for all checker types | [`wiki/conventions/checker-config.md`](../conventions/checker-config.md) |

The runner is built on
[chromedp](https://github.com/chromedp/chromedp) (not Rod, despite
older spec docs sometimes saying so). The two are roughly
substitutable; the choice was driven by chromedp's more direct DevTools
Protocol surface and the team's familiarity with its action-list API.

## Origin

Browser monitoring shipped in March 2026; the design doc lives at
`specs/done/2026/03/2026-03-28-01-browser-monitoring.md` if you need
the original rationale. Subsequent specs polished the dashboard
affordances rather than the runner.
