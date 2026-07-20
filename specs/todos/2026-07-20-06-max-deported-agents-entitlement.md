---
model: sonnet
effort: high
---

# Entitlements have no cap on deported agents — add `maxDeportedAgents` (1/3/6/9 per plan)

## Problem

Deported agents (private locations, shipped v0.5.0 — see
`wiki/features/deported-agents.md`) have no per-org quota. Any org can enroll
an unlimited number of agents: `MintEnrollmentToken`
(`server/internal/handlers/agents/service.go:237`) and the enrollment path in
`server/internal/handlers/agentws/handler.go` (`serveEnrollment` /
`awaitEnroll`) create tokens and agent rows without consulting entitlements.

`EntitlementLimits` (`server/internal/db/models/entitlements_payload.go`)
currently carries exactly three fields — `maxChecks`, `maxUsers`,
`maxChecksPerMinute` — and the SaaS plan ladder in
`../solidping-billing/server/internal/plans/plans.go` (Free / Starter / Pro /
Scale) prices only those. Agents should be a plan lever too:

| Plan | maxDeportedAgents |
|---|---|
| Free | 1 |
| Starter | 3 |
| Pro | 6 |
| Scale | 9 |

## Proposal

### 1. Model + defaults (this repo)

- Add `MaxDeportedAgents *int` (wire key `maxDeportedAgents`, `nil` =
  unlimited) to `EntitlementLimits` in
  `server/internal/db/models/entitlements_payload.go`. Purely additive —
  `Version` stays 1; unknown-key forward-compat already handles old rows.
- Defaults in `server/internal/entitlements/defaults.go`:
  - **Self-hosted**: `nil` (unlimited) — keeps the "Self-hosted: free"
    competitive positioning documented in `wiki/features/deported-agents.md`.
  - **SaaS**: `1`, mirroring the Free SKU (the file's existing comment
    mandates SaaS defaults stay in sync with billing's Free plan).

### 2. Enforcement (this repo)

- Add an `AgentCreateAllowed(ctx, orgUID) error` guard on
  `entitlements.Service`, modeled directly on `CheckCreateAllowed`
  (`server/internal/entitlements/usage.go:59`): resolve, `nil` cap → allow,
  else count the org's **active (non-deleted) agents across all private
  regions** and return a `QuotaError` wrapping `ErrEntitlementExceeded` at or
  above the cap. Same soft-guard race-window semantics as the existing guards
  (count + insert not atomic — acceptable, documented).
- Call it at the point the agent row is created — the enrollment path in
  `server/internal/handlers/agentws/handler.go` (`awaitEnroll` does the
  atomic token-consume + `agents` row create). On rejection the agent gets a
  protocol `error` frame with a clear message (the operator sees it in agent
  logs) and the one-shot token must **not** be consumed, so enrollment can be
  retried after an upgrade or after deleting another agent.
- Also guard `MintEnrollmentToken` with the same check for early UX: minting
  a token when the org is already at cap returns the standard quota error
  (409/`CONFLICT` or the existing quota-error mapping used by check
  creation — reuse whatever `CheckCreateAllowed` callers map to), so the
  dashboard can surface the upgrade prompt before the operator ever starts a
  container. Enrollment-time enforcement remains the correctness point
  (tokens minted under the cap could otherwise over-enroll).

### 3. Usage surface (this repo)

- Add `Agents int` (wire `agents`) to `entitlements.Usage`
  (`server/internal/entitlements/usage.go`) — count of active agents.
- Dashboard Usage page
  (`web/dash0/src/routes/orgs/$org/organization.usage.tsx`) gains a row
  "Private location agents" with `usage.agents` / `limits.maxDeportedAgents`,
  following the existing rows. User-facing copy says **private location**,
  not "deported agent" (naming rule in `wiki/features/deported-agents.md`).
  Add translations for all four locales (`web/dash0/src/locales/*`).

### 4. Billing service (companion change, `../solidping-billing`)

- Add `MaxDeportedAgents` to the billing repo's `entitlements.Limits` and set
  the ladder in `server/internal/plans/plans.go`: Free `1`, Starter `3`,
  Pro `6`, Scale `9`. Update the "ladders must stay monotonic" comment to
  include `1/3/6/9 agents`.
- The billing PUT already round-trips unknown keys through the OSS payload,
  so no wire/version change is needed on either side.
- If the implementation run cannot touch the sibling repo, land the OSS side
  fully (billing simply won't send the field yet — SaaS default `1` still
  applies to Free-tier orgs) and note the pending billing change in the spec
  archive.

### 5. Docs + tests

- Update `wiki/features/entitlements.md` (field table + defaults) and
  `wiki/features/deported-agents.md` (plan-gate row: SaaS ladder 1/3/6/9,
  self-hosted unlimited).
- Tests: payload encode/decode of the new field, per-mode defaults,
  `AgentCreateAllowed` at/under/over cap and with `nil` cap, enrollment
  rejected at cap **without consuming the token**, token mint rejected at
  cap, usage count excludes deleted agents. E2E: usage page shows the new
  row (extend the existing usage/entitlements coverage).

## Open questions

- Whether existing over-cap orgs (already >1 agent on Free) should be
  grandfathered: proposal is the standard soft-cap behavior — existing agents
  keep working, only *new* enrollments are blocked. No reconciliation job.
