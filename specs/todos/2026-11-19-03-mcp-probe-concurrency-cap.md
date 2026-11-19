---
model: opus
effort: medium
---

# MCP probe tools run scripts and browsers on the API node with no concurrency cap, for any mcp:read token

## Problem

Found by the security scan of 2026-11-19 (finding AR2). Severity: medium. Who can trigger it: any holder of an MCP token with `mcp:read`, a viewer's included. On SaaS, that is anyone who signs up.

Spec 2026-11-18-01 (finding AQ1, #933) capped `POST /orgs/:org/checks/test-run`:

- one run per org, under a process cap of 4 (`server/internal/handlers/checktestrun/handler.go:30-36`, `acquire` at `:78`);
- a per-org rate limit of 20 per minute (`server/internal/app/server.go:644-666`, `:1338-1340`);
- at most 2 of the 4 browser slots (`server/internal/checkers/checkbrowser/checker.go:123-178`, `WithTestRunBudget`).

The MCP probes run the same scripts through the same runner, and they get none of these limits:

- `run_js_script`, `fetch_page` and `browser_snapshot` are classed read (`server/internal/mcp/access.go:80-83`). Their descriptions advertise "works with mcp:read tokens" (`server/internal/mcp/tools_jsauthoring.go:62-107`).
- `toolRunJSScript` (`tools_jsauthoring.go:117`) calls `aichecks.Runner.Run`, and `toolBrowserSnapshot` (`:156`) calls `Runner.BrowserSnapshot`:
  - there is no slot, so the number of runs at once has no limit;
  - there is no per-org cap;
  - the context has no `WithTestRunBudget`, so the browsers take ordinary `browserSlots`, the slots scheduled browser checks wait on.
- Each run may last 30 s (`toolScriptTimeout`, `server/internal/aichecks/runner.go:103-105`). Each JS run spawns an isolated child process with a 256 MB memory limit (`server/internal/checkers/checkjs/isolation.go:46`, `:314`).
- The only limit is the generic `/api/v1` rate limiter: 1800 requests per minute per token or IP by default (`server/internal/config/config.go:2109-2111`). That bounds the rate, not the concurrency.

Impact:

- One token can keep dozens of 256 MB child processes alive on the API node, enough to exhaust its memory and CPU for every tenant.
- `browser_snapshot` (or a script that opens a browser) holds all `MaxConcurrentBrowsers` slots. On a node running both `api` and `checks` (the self-hosted default `all`), scheduled browser checks of other orgs then time out and can raise false incidents. AQ1 closed exactly this on the REST route.
- Viewers are refused the REST route on purpose ("a write route, so orgGroup's floor refuses viewers", `server.go:1333-1335`). On MCP, the same capability needs only read access.

## Proposal

1. Move the admission control out of the REST handler into one place both callers share:
   - a small limiter type (process cap, per-org cap, `Retry-After`), say in `server/internal/aichecks` or a new `server/internal/testruns`, built once in `app` and given to both `checktestrun.NewHandler` and the MCP handler (`mcp.Handler.SetScriptRunner` or a new setter);
   - `checktestrun` keeps its current behaviour through it (`MaxPerOrg = 1`, `maxConcurrent = 4`).
2. In `tools_jsauthoring.go`, acquire a slot for the token's org before `Run`, `FetchPage` and `BrowserSnapshot`, and return a tool error ("too many runs in progress, retry in 5 s") when none is free.
3. Wrap the probe contexts with `checkbrowser.WithTestRunBudget`, so MCP browsers count against `MaxTestRunBrowsers`.
4. Apply the same per-org limit of 20 per minute to the three probes, keyed on the token's org.
5. Decide the role floor (see Open questions). At least make the `mcp:read` behaviour explicit in `wiki/` and the tool descriptions.
6. Also wrap the AI authoring loops (`POST …/checks/ai/generate`, `aichecks` loop tool calls) with the shared limiter if they don't already pass through one. Check this under To verify.

## Tests

- `server/internal/mcp/tools_jsauthoring_test.go`, with a blocking fake runner:
  - two concurrent `run_js_script` calls from the same org: the second gets the busy error. This fails before the fix.
  - Positive control: a call from another org still runs while the first org's run is in flight.
- Shared-limiter test: a REST test run and an MCP probe from the same org cannot both be in flight. A REST run in org A does not block an MCP probe in org B.
- `checkbrowser` budget: an MCP `browser_snapshot` context is marked by `IsTestRun`, so it cannot take more than `MaxTestRunBrowsers` slots. Reuse the `checker_test.go` slot tests added by #933.
- The existing `server/internal/handlers/checktestrun/handler_test.go` keeps passing unchanged.

## To verify

- Whether the AI authoring loops (`aichecks` generate/repair) call the runner through any concurrency cap today.
- Whether the MCP handler has the org UID in its context for every token type (PAT, MCP OAuth, session), to key the per-org cap.

## Resolved open questions

- `run_js_script` and `browser_snapshot` require the `user` role, as the REST test run does; `fetch_page` stays at read with the shared cap. A read-only token gets a clear "needs a write-capable token" error. (autopilot: recommended answer)

Original question:

- Should the probes require the `user` role, as the REST test run does? Recommended: yes for `run_js_script` and `browser_snapshot`, which execute arbitrary code and hold a browser. `fetch_page` can stay at read with the shared cap. An AI client with a read-only token then gets a clear "needs a write-capable token" error.
