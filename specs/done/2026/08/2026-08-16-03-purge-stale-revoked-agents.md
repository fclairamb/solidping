---
model: sonnet
effort: high
---

# Revoked agents linger forever in the private-locations agent list

## Problem

On `/dash0/orgs/$org/organization/private-locations`, the **Agents** table keeps
showing agents that were revoked long ago. On `solidping.k8xp.com` / org `acme`
today, the list carries `s3ns-paris` (revoked, last seen 2d 21h ago) and
`s3ns-prod-acme-system` (revoked, last seen 3d 0h ago) alongside the three live
agents. They will stay there forever:

- `RevokeAgent` only flips `status`/`revoked_at`; it never sets `deleted_at`
  ([server/internal/db/postgres/agents.go:272](server/internal/db/postgres/agents.go:272)).
- `ListAgents` filters on `deleted_at IS NULL` only, so revoked-but-not-deleted
  rows keep coming back
  ([server/internal/db/postgres/agents.go:200](server/internal/db/postgres/agents.go:200)).
- The UI renders no delete action for a non-active agent — the `Trash2` button is
  gated on `agent.status === "active"`
  ([web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:456](web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:456)) —
  so there is no way for an admin to clear them from the page at all.
- The existing `agent_gc` job sweeps **only** `kind = 'system'` agents on
  staleness, and deliberately leaves org agents alone
  ([server/internal/jobs/jobtypes/job_agent_gc.go](server/internal/jobs/jobtypes/job_agent_gc.go),
  [ListStaleSystemAgents](server/internal/db/postgres/agents.go:376)). A revoked
  org agent is a different case: nobody is waiting for it to come back.

Side effects of the leak: the region-deletion guard counts enrolled agents
(`ErrRegionHasAgents`,
[server/internal/handlers/agents/service.go:33](server/internal/handlers/agents/service.go:33));
dead rows make the fleet list unreadable; and revoked rows keep a stale X25519
recipient visible in the UI long after it stopped mattering.

## Proposal

**2 days after its revocation**, an agent is purged: it disappears from the
private-locations page and from the API listing.

The clock runs on `revoked_at`, not on `last_seen_at`. Revocation is the
decisive event — an admin cut this agent's access on purpose, and how recently
it phoned in before that says nothing about whether the row should stay.
`last_seen_at` also fails at both ends: an agent revoked while still connected
keeps a fresh `last_seen_at` and would never age out, and an agent revoked
before it ever connected has no `last_seen_at` at all.

### Backend

1. Extend the existing `agent_gc` job rather than adding a second sweep
   ([job_agent_gc.go](server/internal/jobs/jobtypes/job_agent_gc.go)). Add a
   second pass alongside the stale-system-agent pass:
   - New DB method `ListPurgeableRevokedAgents(ctx, cutoff)` — live rows
     (`deleted_at IS NULL`) with `status = 'revoked'` and `revoked_at < cutoff`.
     Guard the legacy/inconsistent case where a revoked row has a NULL
     `revoked_at` by falling back to `updated_at` (`COALESCE(revoked_at,
     updated_at) < cutoff`), so such a row is still eventually collected instead
     of becoming permanently immortal. Both kinds (`org` and `system`) are
     eligible — a *revoked* agent is dead by admin decision regardless of who
     manages it.
   - New DB method `PurgeAgent(ctx, uid)` — soft-delete (`deleted_at`,
     `updated_at`), scoped to `status = 'revoked' AND deleted_at IS NULL` so it
     can never touch a live agent. Keep it a soft delete, matching
     `RetireSystemAgent`: the row still carries the fingerprint referenced by
     historical results/logs.
   - Reuse the existing `retire()` worker-row cleanup path (resolve
     `agentcrypto.WorkerSlug(agent.UID)` → `DeleteWorker`) so the purge also
     clears the leftover workers row.
2. Constant `defaultRevokedPurgeAfter = 2 * 24 * time.Hour`, overridable through
   `AgentGCJobConfig` with a new `revokedPurgeAfterSeconds` field, mirroring the
   existing `staleAfterSeconds` handling. The 6h `agentGCInterval` cadence is
   fine — no new schedule.
3. Implement in **both** `server/internal/db/postgres/agents.go` and
   `server/internal/db/sqlite/agents.go`, and declare in the
   `db.Service` interface ([server/internal/db/service.go:243](server/internal/db/service.go:243)).

### Frontend

The purge alone fixes the reported symptom, but revoked rows are still visible
for up to two days with no way to act on them. Show the `Trash2` action for
revoked agents too, wired to the same `DELETE /api/v1/orgs/:org/agents/:uid`
endpoint so an admin can clear one immediately instead of waiting for the sweep
(the endpoint currently maps to `RevokeAgent`
[handler.go:133](server/internal/handlers/agents/handler.go:133) — on an
already-revoked agent it must purge rather than no-op). Follow the repo's delete
conventions (red `Trash2`, confirmation) and the design reference.

### Tests

- `ListPurgeableRevokedAgents` returns a revoked agent whose `revoked_at` is
  past the cutoff, and **does not** return: an active agent of any age (however
  old its `last_seen_at`), a revoked agent revoked inside the window, or an
  already-deleted row.
- An agent revoked 3 days ago but with `last_seen_at` 1 minute old **is**
  collected — the positive control proving the clock is `revoked_at` and not
  last-seen.
- A revoked row with a NULL `revoked_at` is still collected via the `updated_at`
  fallback.
- `PurgeAgent` is a no-op on an active agent (negative control).
- Job-level test: an agent revoked 3 days ago is gone from `ListAgents` after
  one `agent_gc` run; an agent revoked 1 day ago is still listed.
- Postgres and SQLite parity for the new methods.

## Implementation Plan

1. **DB layer** (`server/internal/db/postgres/agents.go` and
   `server/internal/db/sqlite/agents.go`, kept byte-identical per existing
   convention):
   - `ListPurgeableRevokedAgents(ctx, cutoff time.Time) ([]*models.Agent, error)`
     — `status = 'revoked' AND deleted_at IS NULL AND COALESCE(revoked_at,
     updated_at) < cutoff`. No kind filter (both `org` and `system` eligible).
   - `PurgeAgent(ctx, uid string) error` — soft delete (`deleted_at`,
     `updated_at`) scoped to `uid = ? AND status = 'revoked' AND deleted_at IS
     NULL`. No rows-affected check needed at this layer (mirrors
     `RetireSystemAgent`); the caller that needs "was it found" semantics
     resolves the row first.
   - Add both to the `db.Service` interface
     (`server/internal/db/service.go`), next to the existing agent methods.

2. **`agent_gc` job** (`server/internal/jobs/jobtypes/job_agent_gc.go`):
   - New constant `defaultRevokedPurgeAfter = 2 * 24 * time.Hour`.
   - New `AgentGCJobConfig.RevokedPurgeAfterSeconds int` field +
     `revokedPurgeAfter()` accessor mirroring `staleAfter()`.
   - Extract the worker-row cleanup half of `retire()` into a shared
     `cleanupWorkerRow(ctx, jctx, agent)` helper; `retire()` calls it after a
     successful `RetireSystemAgent`.
   - New `purge(ctx, jctx, agent) bool`: calls `PurgeAgent`, then
     `cleanupWorkerRow` on success.
   - `Run()`: after the existing stale-system-agent pass, add a second pass —
     `ListPurgeableRevokedAgents(ctx, now.Add(-r.revokedPurgeAfter()))`, purge
     each, log a summary count — before the nonce-prune step.

3. **Admin service** (`server/internal/handlers/agents/service.go`):
   `RevokeAgent` branches on the current status: if the agent is already
   `revoked`, call `s.db.PurgeAgent` instead of re-revoking (and skip
   `ResealRegion` — a revoked agent is already excluded from seals). The org
   scoping check (`GetAgent` + `OrgUID() != org.UID` → `ErrAgentNotFound`)
   stays first, so purge is exactly as org-scoped as revoke. The HTTP mapping
   (`DELETE /api/v1/orgs/:org/agents/:uid` → `Handler.RevokeAgent` →
   `Service.RevokeAgent`) is unchanged — no handler edit needed.

4. **Frontend**
   (`web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx`):
   - Drop the `agent.status === "active"` gate on the row's `Trash2` button —
     render it for every status, reusing the same `useRevokeAgent` mutation
     (the backend now purges on a second call).
   - Track the full pending agent (not just its UID) so the confirmation
     dialog can read its status and show purge-appropriate copy ("Remove this
     agent?" / "This clears it from the list immediately, instead of waiting
     for the two-day automatic cleanup.") instead of the revoke copy, when the
     target is already revoked. Button title differs the same way ("Revoke
     agent" vs "Remove agent").
   - No i18n JSON edits needed — `privateLocations.agents.*` keys are not
     present in any `locales/*/org.json` today; every call already runs on its
     inline default string.

5. **Tests**
   - `sqlite/agents_test.go`: `ListPurgeableRevokedAgents` positive/negative
     matrix (past-cutoff revoked → in; active of any age → out; revoked inside
     window → out; already-deleted → out; NULL `revoked_at` past cutoff via
     `updated_at` fallback → in). `PurgeAgent` no-op on an active agent
     (negative control: row survives, still active).
   - `jobtypes/job_agent_gc_test.go`: one job-level test seeding a
     3-day-revoked agent (gone from `ListAgents` after one run, worker row
     soft-deleted) and a 1-day-revoked agent (still listed); a positive
     control with `revoked_at` 3 days ago but `last_seen_at` 1 minute ago
     (still purged — proves the clock is `revoked_at`).
   - `handlers/agents/service_test.go`: a second `RevokeAgent` call on an
     already-revoked agent purges it (disappears from `ListAgents`); org
     scoping still applies (reuse the `ErrAgentNotFound` pattern from
     `TestRevokeAgentIsOrgScoped`).
   - `web/dash0/e2e/private-locations.spec.ts`: extend the existing stubbed
     agents-list pattern (see the last-seen test) with a revoked agent row —
     assert the `Trash2` button renders for it, the confirmation dialog shows
     the "Remove" copy, and confirming issues the `DELETE` call.

6. **QA gate**: `make build-backend lint-back test`, then
   `make build-dash0 && cd web/dash0 && bun run lint`. Run the new/extended
   E2E file if the local devloop is in `SP_RUNMODE=test`; otherwise author it
   and report so in the final QA notes.
