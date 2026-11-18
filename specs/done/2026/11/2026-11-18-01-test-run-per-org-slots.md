---
model: opus
effort: medium
---

# One org can take every test-run slot, and the API node's browser slots with them

## Problem

Found by the security scan of 2026-11-18 (finding AQ1). Severity: Low. Any member with the `user` role in any org can trigger it. On SaaS, anyone who signs up gets their own org, so in practice any account can.

- `POST /api/v1/orgs/:org/checks/test-run` (`server/internal/app/server.go:1311`) runs an unsaved JS check once, on the API node. The concurrency cap lives in the handler: `maxConcurrent = 4` (`server/internal/handlers/checktestrun/handler.go:28`), with one channel built in `NewHandler` (`handler.go:51`). There is one handler for the whole process, so the 4 slots are shared by every org on the instance. A request that finds them all busy gets `429` right away (`handler.go:121-129`).
- Each run can last up to 30 s (`toolScriptTimeout`, `server/internal/aichecks/runner.go:26`, clamp at `runner.go:103-105`). A caller who keeps 4 requests in flight, each sending a new one as soon as the last finishes, holds every slot indefinitely. The global per-IP rate limiter doesn't help: 4 requests every 30 s is far below its budget.
- A script that calls `browser.open()` also holds one of the process-wide browser slots (`MaxConcurrentBrowsers = 4`, `server/internal/checkers/checkbrowser/checker.go:92-100`). So 4 looping test runs also use up the browser pool of that process:
  - AI authoring runs, which use the same runner, wait or time out.
  - With the default node role `all` (self-hosted single binary), or any node that runs both `api` and `checks`, the scheduled browser checks of every org on that node wait for a slot inside their own timeout. They then report a timeout, which can open incidents and send alerts on another tenant's healthy checks.
- What the attacker gets: denial of the test-run feature for every other org, and on a combined node, false browser-check failures for other orgs. Every run stays capped (30 s, egress guard, script size), so it is a fairness and availability gap, not code execution.

## Proposal

1. Keep a small global cap, and add a per-org cap under it. Example: at most 1 test run in flight per org (or 2, see Open questions), and at most 4 in the process. Keep the slot map in the handler, keyed by the org UID that `RequireOrgAccess` resolved, and remove each entry when its run finishes so the map can't grow forever. When an org is at its limit, answer `429 RATE_LIMITED` with `Retry-After` and a message that names the per-org limit (`handler.go:121-129`).
2. Don't let test runs take every browser slot. Either give test runs a browser budget of their own (for example, they may hold at most 2 of the 4 browser slots), or make them wait for a browser slot only for a short time, so scheduled checks keep at least part of the pool. Implement it next to `browserSlots` (`checkbrowser/checker.go:100`), and don't change how scheduled checks wait for a slot.
3. Add a small per-org rate limit on the route (for example, 20 runs per minute per org), with the existing `middleware.NewRateLimiter` pattern used for `/tools/run` (`server.go:1207-1212`), keyed on the org.
4. Document the limits in the JS checks docs (`web/docs/docs/features/javascript-checks.md`) and in the OpenAPI spec for the endpoint.

## Tests

- `server/internal/handlers/checktestrun/handler_test.go`: with a stub runner that blocks, org A fills its per-org quota and its next request gets `429`. Then a request from org B, with org A still holding its slots, gets `200`. That is the regression test: before the fix, org B gets `429`. Positive control: once org A's runs finish, org A gets `200` again, and the per-org map is empty after all runs finish.
- `server/internal/checkers/checkbrowser`: with every test-run browser slot held, a scheduled browser execution still gets a slot (or test runs can't take the last ones), depending on which option of step 2 is chosen. Positive control: with nothing held, a test run gets a browser slot at once.
- The router test that builds the real chain: the per-org limiter answers `429` after its budget, and a viewer still gets `403` (the existing assertion in `server/internal/app/check_test_run_route_test.go` keeps passing).

## Open questions

- Per-org concurrency: 1 or 2? **Recommended:** 1. The dash0 Test button runs one script at a time, and 1 is enough for a human. A CI job that tries several scripts gets a clear `429` with `Retry-After`.
- Should the per-org limits depend on the plan (entitlements)? **Recommended:** not now. Fixed limits fix the fairness gap. A plan-based limit can come later if a paying customer needs more.

## Resolved open questions

- Per-org concurrency: allow **1** test run in flight per org. (autopilot: recommended answer)
- Per-org limits are fixed, not plan-based (no entitlements) for now. (autopilot: recommended answer)
- Proposal step 2: give test runs a browser budget of their own: they may hold at most **2** of the 4 browser slots, so scheduled checks always keep at least 2. Scheduled checks wait for a slot exactly as today. (autopilot: the spec's first option and example)
