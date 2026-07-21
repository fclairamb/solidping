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

## Implementation Plan

Scope: this repo only. `../solidping-billing` (item 4 in the Proposal) is out
of reach for this run — noted as pending in the final report.

1. **Model** (`server/internal/db/models/entitlements_payload.go`): add
   `MaxDeportedAgents *int` (`maxDeportedAgents,omitempty`) to
   `EntitlementLimits`; add the same field to the strict wire struct inside
   `UnmarshalJSON` (plain field, no alias — unlike `maxUsers`/`maxSsoUsers`).
   No `Version` bump. Update the struct's doc comment.

2. **Defaults** (`server/internal/entitlements/defaults.go`): add
   `defaultMaxDeportedAgentsSaaS = 1`; set it on the SaaS branch of
   `DefaultsFor`; leave self-hosted's `Limits` untouched (field absent ⇒ nil ⇒
   unlimited), with a comment explaining why.

3. **Resolver merge** (`server/internal/entitlements/service.go`): propagate
   `MaxDeportedAgents` in `Service.merge` alongside the other three limits.

4. **Usage + enforcement** (`server/internal/entitlements/usage.go`):
   - Add `Agents int` (`json:"agents"`) to `Usage`.
   - Add a shared `countActiveAgents(ctx, orgUID)` helper (uses the existing
     `db.Service.ListAgents`, filters `Status == models.AgentStatusActive` —
     no new db-layer method needed).
   - Wire it into `Usage()`.
   - Add `AgentCreateAllowed(ctx, orgUID) error`, modeled on
     `CheckCreateAllowed`: nil cap ⇒ allow; else compare `countActiveAgents`
     against the cap and return `&QuotaError{LimitName: "MaxDeportedAgents", ...}`.

5. **Admin API wire-through** (`server/internal/handlers/entitlements/handler.go`):
   add `MaxDeportedAgents` to `overlayLimits` so PATCH merges it like the other
   three limits.

6. **Enrollment-token mint guard** (`server/internal/handlers/agents/service.go`
   + `handler.go`): add an `ent *entitlements.Service` field to `agents.Service`
   (new `NewService` parameter), call `AgentCreateAllowed` first thing in
   `MintEnrollmentToken` (mirrors `CheckCreateAllowed`'s placement at the top of
   `checks.Service.CreateCheck`); map `ErrEntitlementExceeded` to `402
   QUOTA_EXCEEDED` in `writeServiceError` exactly like `checks/handler.go`'s
   `handleCreateError`. Update the `server.go` call site
   (`agentsadmin.NewService(s.dbService, s.services.Credentials,
   s.services.Entitlements)`) and the existing `agents/service_test.go` setup
   (pass `nil` to keep current tests behavior-identical).

7. **Enrollment guard** (`server/internal/handlers/agentws/handler.go`): in
   `awaitEnroll`, after the structural frame checks and before
   `EnrollAgent`'s atomic consume, look the token up again (non-consuming, via
   the existing `GetAgentEnrollmentTokenByHash`) to learn the org, then call
   `entitlements.AgentCreateAllowed`. On rejection: write an `error` frame
   (`Code: QUOTA_EXCEEDED`) and return WITHOUT calling `EnrollAgent` — the
   token stays unconsumed and retryable. Guard is skipped when
   `h.entitlements == nil` (existing optionality pattern already used for
   `ReserveCheckExecution`).

8. **Frontend**: add `EntitlementsUsage.agents` and
   `EntitlementsLimits.maxDeportedAgents` to `web/dash0/src/api/hooks.ts`; add a
   `UsageRow` for "Private location agents" in
   `organization.usage.tsx`; add `usage.privateLocationAgents` to all four
   locale files (`en`/`de`/`fr`/`es` `org.json`). Copy says "private location",
   never "deported agent" (naming rule in `wiki/features/deported-agents.md`).

9. **Docs**: update `wiki/features/entitlements.md` (limits table, defaults
   table, Usage field table), `wiki/api-specification/entitlements.md` (wire
   field list), `wiki/database-model/entitlements.md` (payload column
   description — also fixing its stale `maxSsoUsers` mention while touching
   the line), and `wiki/features/deported-agents.md` (plan-gate cell in the
   competitive table + the "no plan gate" bullet, reflecting the SaaS
   1/3/6/9 ladder vs. self-hosted unlimited).

10. **Tests**:
    - `entitlements_payload_test.go`: encode/decode of `maxDeportedAgents`.
    - `entitlements/service_test.go`: `DefaultsFor` (SaaS=1, self-hosted=nil),
      `AgentCreateAllowed` unlimited/under/at cap, `Usage.Agents` excludes
      revoked/deleted agents.
    - `handlers/entitlements/handler_test.go`: PUT round-trip of
      `maxDeportedAgents` (mirrors `TestPutAcceptsMaxChecks`).
    - `handlers/agents/service_test.go`: `MintEnrollmentToken` rejected at cap
      (402-mappable error), allowed under cap.
    - `handlers/agentws/handler_test.go`: enrollment rejected at cap WITHOUT
      consuming the token (token remains usable — or at least the agent row
      count doesn't grow — after a rejected attempt); enrollment allowed under
      cap.
    - dash0 E2E (`entitlements-usage.spec.ts`): bump the "three rows" assertion
      to four; assert the new row's label renders.

11. **QA**: `make build-backend lint-back test`, `make build-dash0`,
    `cd web/dash0 && bun run lint`. Re-run any flaky Postgres testcontainer
    package with `-p 1` before treating it as a regression.
