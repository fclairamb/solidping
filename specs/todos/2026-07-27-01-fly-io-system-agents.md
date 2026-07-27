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
  by system agents keep fair scheduling. Locate the direct-path accounting in
  `DirectBackend.SubmitResult` / `checkworker/worker.go` and hoist it into
  the shared server-side path (`handlers/workers/service.go:SubmitResult`)
  so both backends hit it.

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

### Open questions

- EWMA hoisting may reveal that some accounting depends on in-process state
  of the worker loop; if so, scope item 4 down to what the server can
  compute from the submitted result alone and note the residual gap.
