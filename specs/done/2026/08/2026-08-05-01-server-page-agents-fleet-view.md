---
model: sonnet
effort: medium
---

# No view exists over all deployed agents — add a fleet-wide Agents tab to the server page

## Problem

Since the fly.io system agents shipped (v0.7.0, `specs/done/2026/07/2026-07-27-01-fly-io-system-agents.md`), agents of `kind='system'` enroll with no owning organization and are **listed nowhere**:

- The only list endpoint is org-scoped: `GET /api/v1/orgs/:org/agents` → `agents.Handler.ListAgents` (`server/internal/handlers/agents/handler.go:112`) → `ListAgents(ctx, orgUID)` (`server/internal/db/sqlite/agents.go:200`), which filters by `organization_uid` and therefore can never return system agents.
- There is no `ListAllAgents` / `ListSystemAgents` DB method at all — the only system-agent read is the stale-cutoff query used by the GC job (`ListStaleSystemAgents`, `server/internal/db/sqlite/agents.go:361`).
- `wiki/features/deported-agents.md:328-330` states this explicitly: system agents "are listed nowhere and are reaped only by the `agent_gc` job. A `mgmt` read view is deliberately out of scope so far."

Operationally this means the operator cannot answer "what agents are connected right now, from where, and when were they last seen?" without querying the database by hand. The server page (`web/dash0/src/routes/orgs/$org/server.tsx`, superadmin-gated) is the natural home: system agents belong to no org, and the server section is the existing surface for instance-wide state. The nearby `server.performance.tsx` tab already lists *workers* via `useSchedulingLaneLoad()`, but that shows scheduling lane load, not agent identity/enrollment/liveness — complementary, not a substitute.

## Proposal

Read-only fleet view first; management actions stay out of scope.

### Backend

1. **New DB method** `ListAllAgents(ctx)` in both `server/internal/db/sqlite/agents.go` and `server/internal/db/postgres/agents.go` (mirror implementations, per `sync-pg-to-sqlite` discipline): all non-deleted agents, both kinds, ordered by `kind, region, name`. Include revoked agents (status column makes them distinguishable); exclude soft-deleted.
2. **New superadmin endpoint** `GET /api/v1/system/agents`, wired into the existing `/api/v1/system/*` superadmin group (`server/internal/app/server.go:1058-1084`). Response wrapped in `{ "data": [...] }`.
3. **Extended response shape**: reuse `AgentResponse` (`server/internal/handlers/agents/service.go:401-411`) but add the fields the org-scoped view omits: `kind` (`org`|`system`) and `org` (owning org slug/uid, null for system agents). Keep camelCase.
4. Handler tests: system + org agents both returned; non-superadmin gets 403.

### Frontend (dash0)

5. **New tab** `server.agents.tsx` added to the `TabNav` in `server.tsx:16-27`, with `tabs.agents` labels in all four locales (`web/dash0/src/locales/{en,fr,de,es}/server.json`).
6. Table modeled on the existing `AgentsCard` (`web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:355-475`): columns **Kind / Org / Name / Region / Fingerprint / Last seen / Status**. Use `regionDisplayLabel` (`web/dash0/src/lib/region-label.ts`) for regions and the same relative "last seen" rendering; 30s refetch like `useAgents` (`web/dash0/src/api/hooks.ts:4902`).
7. **Staleness signal**: highlight agents whose `lastSeenAt` is old (e.g. > 5 min = warning, since connected agents refresh `last_seen_at` on every ping tick — `server/internal/handlers/agentws/handler.go:482-524`). The GC job retires system agents only after 7 days of silence (`server/internal/jobs/jobtypes/job_agent_gc.go:25`), so without a visual cue a dead-for-days agent looks fine in the list.
8. Follow the design reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for table/badge primitives; page must work on mobile.

### Out of scope (note in PR if tempting)

- Revoking system agents from this view — `RevokeAgent` deliberately can't match system agents today (`server/internal/handlers/agents/service.go:460-463`); revocation path is removing the token from `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`. Changing that is a separate decision.
- Merging with the workers/lane-load view on `server.performance.tsx` — different concern (scheduling capacity vs agent fleet identity). A follow-up could cross-link them.
- OpenAPI documentation of the agent API (currently zero "agent" mentions in `openapi.yaml`) — worth doing but a distinct chore.

### Update the wiki

Amend `wiki/features/deported-agents.md:328-330` — the "listed nowhere / deliberately out of scope" note becomes stale once this ships.
