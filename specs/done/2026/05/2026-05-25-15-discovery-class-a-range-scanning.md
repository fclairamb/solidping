# Support /8 (Class A) range scanning in network discovery

## Context

Network discovery (`/dash0/orgs/$org/discovery/new`) scans CIDR ranges via a
fan-out design: a `network_discovery_plan` job splits the requested CIDRs into
≤/20 (4096-address) chunks and schedules one bounded `network_discovery` child
job per chunk; the detail page aggregates per-chunk progress.

A `10.0.0.0/8` scan (16,777,216 addresses = exactly 4096 chunks) is rejected
today: `ValidatePlanCIDRs` caps a scan at `MaxScanChunks = 256` chunks (~1M, a
/12) — `server/internal/discovery/safety.go:20`. The handler maps the resulting
`ErrRangeTooLarge` to HTTP 422 `DISCOVERY_RANGE_TOO_LARGE`
(`server/internal/handlers/discovery/handler.go:142`).

Goal: allow a full /8. Raise the hard ceiling to exactly admit /8, keep the slow
background-scan execution model unchanged, and add a client-side size estimate +
warning on the new-scan form so users understand the scale before submitting.

## Goal

- `10.0.0.0/8` submitted on the new-scan page is accepted and fans out into 4096
  child chunks that scan progressively in the background.
- Ranges larger than /8 (e.g. /7) still return 422 `DISCOVERY_RANGE_TOO_LARGE`.
- The new-scan form shows an estimated host/chunk count and a long-duration
  warning for large ranges; submission stays allowed after the existing
  confirmation checkbox.

## Non-goals

- No throughput/parallelism changes — scan still runs at the default worker pool
  (2) and per-chunk concurrency (64); a /8 takes ~1–2 days. The form already
  shows the amber `workerNote`.
- No configurable ceiling — `MaxScanChunks` stays a constant (raised to 4096).
- No change to the 30-min `staleScanThreshold` "already-running" guard
  (`service.go:30`). Known pre-existing limitation: it relies on a child being
  actively running to keep blocking new scans; not touched here.
- No IPv6 support (still rejected).
- No hard UI cap — the warning is advisory; the backend remains the authority.

## Backend changes

### `server/internal/discovery/safety.go`

- Raise `MaxScanChunks` from `256` to `4096`. 4096 chunks × 4096 addresses =
  16,777,216 = exactly one /8. A /7 (8192 chunks) still exceeds and is rejected.
- Update the `MaxScanChunks` doc comment (lines 17–19) to the new rationale:
  4096 chunks ≈ a /8 ceiling; larger ranges are rejected.
- No other logic changes — `ValidatePlanCIDRs`, `SplitCIDRs`, the plan-job
  re-validation (`job_network_discovery_plan.go:32`), and the per-chunk
  `ValidateCIDRs` all key off these constants.

Note: the plan job schedules children in a loop
(`job_network_discovery_plan.go:77-96`), so a /8 issues 4096 sequential
`CreateJob` inserts on one worker run. This is acceptable at this scale (fast
inserts, runs once); no batching required.

## Frontend changes

### `web/dash0/src/routes/orgs/$org/discovery.new.tsx`

- Add a small pure CIDR-size estimator: for each token parsed from `cidrsText`
  (already split on `/[\n,]+/`), match valid `a.b.c.d/NN` IPv4 CIDRs and compute
  `hosts = 2 ** (32 - prefix)`. Sum across tokens; ignore unparseable/partial
  tokens. Derive `estimatedChunks = Math.ceil(totalHosts / 4096)`.
- Render an `Alert` (`@/components/ui/alert`, per the design-reference) above the
  confirm checkbox when the scan will be chunked (`totalHosts > 4096`):
  `variant="warning"`, showing estimated host count, chunk count, and a
  "may take a long time" note.
- Submission stays gated only by the existing confirm checkbox — no hard block.

### `web/dash0/src/locales/{en,fr,es,de}/discovery.json`

Add a key (e.g. `largeRangeWarning`) with `{{hosts}}` / `{{chunks}}`
interpolation; provide en/fr/es/de translations.

## Tests

### Backend — `server/internal/discovery/safety_test.go`

- Update `TestValidatePlanCIDRs`: `/8` (4096 chunks) now accepted at the ceiling;
  `/7` (8192 chunks) exceeds → `ErrRangeTooLarge`. Replace the stale
  `/12 at ceiling` / `/11 exceeds` cases with the new boundaries; keep a small
  "ok" range case.
- Grep for any other test asserting the old 256 / `/12` boundary (handler/service
  tests) and update.

### Frontend — `web/dash0/e2e/`

- Playwright: on the new-scan page, entering `10.0.0.0/8` shows the large-range
  warning Alert with a host/chunk estimate; Start enables once the confirm
  checkbox is checked.

## Verification

```bash
make lint && make gotest && make build
```

Manual (`make dev-test`):

1. Open `http://localhost:4000/dash0/orgs/default/discovery/new`.
2. Enter `10.0.0.0/8` → warning Alert shows ~16.7M hosts / 4096 chunks; check
   confirm; Start.
3. Detail page shows `0 / 4096 chunks` and progresses; hosts stream in.
4. API: `POST /api/v1/orgs/default/discovery/scans` with
   `{"cidrs":["10.0.0.0/8"]}` → 201; `{"cidrs":["10.0.0.0/7"]}` → 422
   `DISCOVERY_RANGE_TOO_LARGE`.

## Implementation Plan

1. **Backend ceiling** — bump `MaxScanChunks` to 4096 + update comment in
   `safety.go`. Commit: `feat: raise discovery scan ceiling to admit a full /8`.
2. **Backend tests** — update `safety_test.go` boundaries (and any other test
   asserting the old ceiling). Commit:
   `test: cover /8 at ceiling and /7 over ceiling for discovery`.
3. **Frontend estimate + warning** — CIDR estimator + warning Alert in
   `discovery.new.tsx`; i18n keys in the four `discovery.json` files. Commit:
   `feat: warn with host/chunk estimate for large discovery ranges`.
4. **Frontend test** — Playwright assertion for the /8 warning. Commit:
   `test: e2e large-range warning on discovery new-scan`.
5. **QA** — `make lint && make gotest && make build` (+ `make test-dash`);
   fix until green; archive spec to `specs/done/2026/05/`.
