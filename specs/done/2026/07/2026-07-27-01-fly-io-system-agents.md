---
model: opus
effort: xhigh
---

# Cloud workers can't run outside the cluster: generalize deported agents into platform-operated "system agents" (fly.io)

## Problem

We want to run SolidPing-operated cloud check workers on fly.io (and similar
outside-the-cluster hosts). Today the cloud worker path (`SP_NODE_ROLE=checks`)
requires a direct PostgreSQL connection: `DirectBackend` claims jobs with
`SELECT … FOR UPDATE SKIP LOCKED` and writes results/incidents/leases straight
to the database (`server/internal/checkworker/backend/direct.go`). Exposing
Postgres to fly machines is a security regression and a performance trap
(claim transactions would carry a full cross-continent RTT).

The right transport already exists: deported agent mode (`SP_NODE_ROLE=agent`,
spec `2026-07-16-02`) runs the same `CheckWorker` loop with **zero DB access**
— outbound-only WebSocket to `GET /api/v1/agent/ws`, Ed25519-signed
reconnects, pull-based claiming, results written server-side via
`workers.Service.SubmitResult`. But agent mode is hard-scoped to
*tenant-private* regions, which blocks platform use in three places:

1. **Claim scoping** — `ClaimJobsForAgent` filters
   `organization_uid = ? AND region = ?` with exact equality
   (`server/internal/checkworker/checkjobsvc/service.go:311-315`). An agent
   physically cannot serve a shared cloud region across orgs.
2. **Enrollment binds to an org** — `agents.organization_uid` is NOT NULL
   (`server/internal/db/postgres/migrations/006_v0_5_0.up.sql:77`), and
   enrollment tokens are minted per `(org, private-region)` by org admins via
   `regions.PrivateRegionSlug`. A SolidPing-operated worker has no owning org.
3. **Config delivery** — private agents receive `config_sealed` (age/X25519
   envelopes encrypted client-side at check save). Cloud checks are stored
   plaintext and have no sealed envelope to ship.

Secondary gaps that matter once shared cloud regions are served over the
agent path (noted in `wiki/features/deported-agents.md:237-243`):

- Agent-submitted results don't feed the cost/delay EWMAs and scheduler
  accounting that `DirectBackend` maintains — fair scheduling would degrade
  on any region migrated to fly.
- The reconnect nonce-replay cache is per-server-instance in-memory —
  acceptable with one replica, unsafe if the API tier scales out.
- Fly secrets are app-wide, so per-machine `SP_AGENT_KEYS` doesn't work for
  multi-machine regions; enrollment tokens are currently strictly one-shot
  (`agent_enrollment_tokens`, consumed atomically), so a fleet that enrolls
  on boot can't share a token, and enroll-on-boot leaves dead agent rows
  behind on every machine replacement.

## Proposal

Generalize deported agents into two kinds — keep the wire protocol
(`server/internal/agents/protocol.go`) unchanged wherever possible.

### 1. Data model: `agents.kind`

- Add `agents.kind text not null default 'org' check (kind in ('org','system'))`
  and make `organization_uid` nullable, with a check constraint tying the two
  (`kind = 'org'` ⇔ `organization_uid is not null`). Same for
  `agent_enrollment_tokens` (system tokens bind to a **cloud region slug**
  from the `regions` system parameter instead of an org private region).
- System enrollment tokens are **multi-use and revocable** (add `max_uses
  null` = unlimited + `use_count`, or simply exempt `kind='system'` from the
  one-shot consume): each fly machine generates its own keypair and enrolls
  at boot, so no private key is ever shared between machines. Org tokens stay
  strictly one-shot — no behavior change.
- Minting surface: system tokens are platform-operator material, not org
  admin material. Expose via a system-parameters-seeded flow or a
  `mgmt`-level endpoint — NOT the existing org-admin
  `/orgs/:org/agent-enrollment-tokens` routes. Follow the pattern used for
  other system parameters (`server/internal/app/saas.go` seeding style),
  e.g. `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS` seeding hashed tokens bound to
  region slugs.

### 2. Claim scope for system agents

- In `ClaimJobsForAgent` (`checkjobsvc/service.go:287`), branch on agent
  kind: system agents claim `region = ?` across **all** orgs — the same
  scope `DirectBackend` uses — reusing the existing lease semantics, limit,
  `maxAhead`, and `retryInMs` hint computation untouched.
- `jobs-available` push hints (`agentws`) must fan out to system agents for
  any job in their region regardless of org.
- Validate the token's region against cloud regions
  (`regions.ValidateWorkerRegion` path) at enrollment, mirroring what
  `server/internal/app/server.go:2018-2030` does for in-cluster workers.

### 3. Config delivery: seal at claim

- For jobs handed to a system agent, the server seals the plaintext check
  config to the agent's X25519 public key at claim time, producing the same
  envelope format the agent already decrypts for `config_sealed`. One wire
  format, no plaintext-config branch in the protocol, and defense-in-depth
  beyond TLS.
- Cache sealed envelopes per (check version, agent) only if profiling shows
  the age encryption on the claim path matters; start without caching.

### 4. Result accounting parity

- Route agent-submitted results through the same cost/delay EWMA and
  scheduler-lane updates the direct path performs, so shared regions served
  by system agents keep fair scheduling. Today the worker computes the new
  EWMAs, effective deadline, and lane in `buildSubmitRequest`
  (`server/internal/checkworker/worker.go:1213`) and only `DirectBackend`
  persists them; the WS `result` frame drops the `Sched` block entirely and
  the server-side `SubmitResult` (`handlers/workers/service.go:141-152`)
  just releases the lease.
- Hoist the computation into the shared server-side path
  (`handlers/workers/service.go:SubmitResult`) so both backends hit it. The
  cost sample is derivable from the submitted result (duration, timeout
  status). For the delay sample, **the agent transmits `execStart`** (the
  wall-clock probe start) in the WS `result` frame; the server computes the
  delay sample from it exactly as `delaySampleMs` does today
  (`worker.go:1259`, floored at 0). Accept agent clock skew on this number:
  the delay EWMA is telemetry-only and never steers claim order or lanes
  (spec 2026-07-01-02 D4), so a skewed sample cannot affect scheduling —
  the floor-at-0 already absorbs backwards clocks.

### 5. Fleet hygiene

- **Agent GC**: a periodic job that soft-deletes (revokes) system agents with
  `last_seen_at` older than a configurable window (default e.g. 7 days) and
  cleans their `workers` rows. Enroll-on-boot fleets churn rows; this keeps
  the list meaningful. Org agents are user-managed — exclude them, or gate
  behind a much longer window.
- **Nonce replay cache**: move from per-instance memory to a shared store
  (small table with TTL cleanup, or piggyback on `tls_storage`-style
  key-value from migration 009) — or explicitly document the single-replica
  constraint and defer. Decide in-plan; don't silently ship the in-memory
  cache into a multi-replica deployment.

### 6. Deploy reference

- Add `deploy/fly/` with a `fly.toml` template per region (auto-stop
  disabled, `min_machines_running >= 1`), a short README mapping fly region
  codes (`cdg`, `iad`, …) → the existing SolidPing region slugs, and the secret
  set: `SP_AGENT_SERVER_URL`, `SP_AGENT_ENROLLMENT_TOKEN` (multi-use system
  token), `SP_NODE_ROLE=agent`, `SP_AGENT_NAME` from `FLY_MACHINE_ID`.
- Document in `wiki/features/deported-agents.md` and the docs site
  (private locations page gets a sibling "platform regions on fly.io"
  internal note; user-facing docs unchanged — system agents are invisible to
  customers).

### Non-goals

- No change to org/private agent behavior, enrollment UX, or the dashboard
  agents UI (system agents may be listed later behind a mgmt surface; out of
  scope here).
- No HTTP worker API resurrection (deleted deliberately in `2026-07-16-02`;
  tombstone at `server/internal/app/server.go:792-798`).
- No scheduler redesign — only parity of the existing accounting.

### Decisions

- **System-token minting is env-seeded system parameters only** (e.g.
  `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS` seeding hashed tokens bound to region
  slugs, following the `saas.go` seeding pattern). No `/api/mgmt/...`
  endpoint in this spec.
- **Region names stay the same**: system agents serve the existing cloud
  region slugs, making fly a drop-in replacement per region. No `fly-*`
  slugs, no migration of checks' region sets, no customer-visible churn.
  (Update the `deploy/fly/` README accordingly: it maps fly region codes →
  the *existing* SolidPing region slugs.)
- **EWMA inputs travel with the result**: the WS `result` frame carries
  `execStart` so the server-side hoisted accounting (item 4) can compute the
  delay sample; no server-side approximation of probe start time.

---

## Implementation Plan

Backend-only (Go + SQL + internal docs). No dash0/status0/docs-site surface is
touched — system agents are invisible to customers (Non-goals).

### P1 — Data model: `agents.kind` (proposal 1)

- Append a **`fly.io system agents`** block to the open release migration
  `009_v0_8_0.{up,down}.sql` for **both** engines (the file's own header says
  "append new blocks at the end"; 009 is the current unreleased release
  migration, created by spec 2026-07-26-01).
  - `agents`: `+ kind text not null default 'org'`, `organization_uid` becomes
    nullable, `check (kind in ('org','system'))` and
    `check ((kind='org') = (organization_uid is not null))`, plus an index on
    `(kind, region)`.
  - `agent_enrollment_tokens`: same `kind` / nullable-org treatment, plus
    `max_uses integer` (NULL = unlimited) and `use_count integer not null
    default 0`.
  - `agent_nonces (agent_uid, nonce, seen_at)` — the shared reconnect-replay
    store (see P7).
  - SQLite has no `ALTER COLUMN`, so both agent tables are rebuilt with the
    established `*_new` + `insert select` + `rename to` pattern.
- Models: `Agent.Kind`, `Agent.OrganizationUID *string` (+ `IsSystem()` /
  `OrgUID()` helpers), `AgentEnrollmentToken.{Kind,MaxUses,UseCount}` and a
  nullable `OrganizationUID`. New constructors for the system variants.
- DB layer (postgres + sqlite twins):
  - `GetAgentEnrollmentTokenByHash` becomes kind-aware — an `org` token must
    still be unused; a `system` token stays valid while
    `max_uses is null or use_count < max_uses`.
  - `EnrollAgent` keeps the org path byte-for-byte (atomic single-use consume)
    and adds a system path that increments `use_count`, records the latest
    `used_at`/`used_by_agent_uid`, and creates a `kind='system'`,
    `organization_uid = NULL` agent.
  - `ListAgentEnrollmentTokens` / `ListAgents` stay org-filtered, so system
    rows never surface on the org-admin API (Non-goals).

### P2 — System-token minting: env-seeded only (proposal 1, Decisions)

- `server/internal/app/systemagents.go`: `SeedSystemAgentEnrollmentTokens`
  reads `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS` (`region=spe_…` pairs, comma
  separated — read manually, koanf collapses underscores), validates each
  region with `regions.ValidateWorkerRegion` (so a `@private` slug or an
  undefined region is refused at boot), and upserts one hashed, multi-use
  (`max_uses` NULL) token row per region. Tokens no longer present in the env
  are soft-deleted → removal from the fly secret is the revocation path.
- Called from `main.go` next to `SeedSaaSEntitlements`. No `/api/mgmt` endpoint
  and no org-admin route (Decisions).

### P3 — Claim scope for system agents (proposal 2)

- `checkjobsvc.AgentScope{OrgUID, Region, System}` replaces the loose
  `(orgUID, region)` pair on `ClaimJobsForAgent`. A system scope drops the
  `organization_uid = ?` predicate from **both** the claim SELECT and the
  next-eligible hint; the region predicate, lease semantics, `limit`,
  `maxAhead`, per-job clamp and `retryInMs` are untouched. Fail-closed:
  `System=false` with an empty `OrgUID` (or `System=true` with a private
  `@…` region) is rejected, never widened.
- `agentws` builds the scope from `agents.kind`, and the result-scope guard in
  `handleResult` accepts any org's job whose region equals a system agent's
  region (still exact-region for both kinds).
- Enrollment validates a system token's region against the cloud regions
  (`regions.ValidateWorkerRegion`), mirroring `server.go`'s in-cluster worker
  check.
- `jobs-available` already fans out unconditionally to every connected agent
  (a global `check.created` listen) — verified by test rather than changed.

### P4 — Config delivery: seal at claim (proposal 3)

- Extract `checkjobsvc.OpenJobSecrets` from `MergeJobSecrets` (same failure
  taxonomy, no mutation) so a caller can obtain the plaintext secret map
  without merging it into the wire config.
- `agentws` gains the credentials service. For a **system** agent, each claimed
  job's server-side envelope is opened and re-sealed to that agent's X25519
  recipient with `credentials.SealForRecipients`, shipped in the existing
  `configSealed` field — one wire format, no plaintext-config branch. The same
  re-seal is applied to a tunneled job's SSH block. An envelope that cannot be
  opened drops the job from the batch and records the explicit error result
  (the established decision-6 contract). No caching (spec: start without).

### P5 — Result accounting parity (proposal 4)

- Move the post-exec math into one place: `scheduling.Params.PostExecState`
  (cost sample incl. the timeout pin, delay sample floored at 0, EWMA folds,
  effective deadline, hysteresis lane). `CheckWorker.buildSubmitRequest` is
  rewritten on top of it (identical behavior, covered by a parity test).
- `execStart` travels with the result: `agents.ClientFrame.ExecStart`,
  `backend.SubmitResultRequest.ExecStart`, `workers.SubmitResultRequest
  {ExecStart, FromProbe}`.
- `workers.Service.SubmitResult` (the shared server-side path both the agent
  transport and its internal dispatch-error callers use) now computes the
  state and calls `ReleaseLeaseWithSchedulingState` for probe results;
  dispatch-error results (tunnel drop, seal drop) keep the plain release, and a
  probe result without `execStart` (older agent) folds cost/lane but leaves the
  delay EWMA untouched.
- `workers.NewService` takes the config so it can build `scheduling.Params`
  from `server.scheduling` exactly like the in-process worker.

### P6 — Fleet hygiene: agent GC (proposal 5)

- New job type `agent_gc` (registry + startup seeding + 6 h self-reschedule).
  It revokes and soft-deletes `kind='system'` agents whose `last_seen_at`
  (falling back to `enrolled_at`) is older than a configurable window
  (`staleAfter`, default 7 days), soft-deletes their `workers` rows, and prunes
  expired `agent_nonces`. Org agents are user-managed and never touched.

### P7 — Nonce replay cache: decision (proposal 5)

- **Decision: shared store, not deferred.** Fly regions are multi-machine and
  the API tier can scale out, so the per-instance cache is replaced by the
  `agent_nonces` table (insert-or-conflict, per-agent stale prune before
  insert, global prune in the GC job). `agents.NonceGuard` is the seam; the
  in-memory `NonceCache` stays as the test/no-DB implementation. Fail closed on
  a store error.

### P8 — Deploy reference + docs (proposal 6)

- `deploy/fly/fly.toml` (auto-stop off, `min_machines_running >= 1`) +
  `deploy/fly/README.md`: fly region code → the **existing** SolidPing region
  slugs (Decisions), the secret set (`SP_AGENT_SERVER_URL`,
  `SP_AGENT_ENROLLMENT_TOKEN`, `SP_NODE_ROLE=agent`,
  `SP_AGENT_NAME=$FLY_MACHINE_ID`), and the multi-machine story.
- `wiki/features/deported-agents.md`: a "System agents (platform regions)"
  section and an updated known-gaps list (gaps 1 and 4 are closed here).
- The docs site is deliberately untouched: it is user-facing and system agents
  are invisible to customers.

### QA

`make build-backend lint-back test`, with per-package `go build` / `go test` /
`golangci-lint run` while iterating. New tests: kind-aware enrollment
(multi-use system vs one-shot org), cross-org system claim + org-agent
negative control, seal-at-claim round trip (agent identity opens it; no
plaintext secret on the wire), `PostExecState` parity + server-side EWMA
persistence, GC job selectivity, shared nonce replay across handler instances,
and env-seeding (parse, idempotency, revoke-on-removal).
