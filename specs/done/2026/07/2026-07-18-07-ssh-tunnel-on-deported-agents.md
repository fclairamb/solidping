---
model: opus
effort: xhigh
---

# SSH tunnels work on deported agents (region-allocated SSH check, sealed dispatch)

## Problem

A tunneled check dispatched to a deported agent fails at execute time:

```
level=WARN msg="Tunnel setup failed; skipping probe" component=check_worker
  check_uid=4d7dda60-… error="resolve: ssh tunnel: resolver is not configured"
```

This is the deliberately-deferred gap from the parent spec
(`specs/done/2026/07/2026-07-16-04-ssh-tunnel-via-check-dependency.md`,
decision 6): tunnel resolution happens at execute time through
`sshtunnel.ResolverFunc`, and only the main server wires it
(`server/internal/app/server.go:321` — it loads the referenced SSH check from
the DB and decrypts its `config_private`). Agent mode
(`server/internal/agentmode/`) wires nothing: an agent has no DB access and no
org credential keys, so `sshtunnel.Resolve` finds a nil resolver and returns
`ErrNotConfigured` (`server/internal/integrations/sshtunnel/sshtunnel.go:48`).
The worker correctly classifies it as a tunnel failure and skips the probe —
every execution, forever.

The package was built with the fix's seam already in place: `Resolver` is an
interface-shaped func, `WithResolver`/`ResolverFrom` allow per-context
override, and `CarryResolver` survives the worker's context detach
(`sshtunnel.go:138-185`). What is missing is (a) getting the bastion's
credentials to the agent, and (b) wiring a resolver there that uses them.

## Decisions (settled in design discussion — do not re-litigate)

1. **The referenced SSH check must itself be allocated to the agent's
   region.** For every **private region** (`@<slug>`, see
   `server/internal/regions/regions.go:37`) in the dependent check's
   `regions`, the referenced SSH check's `regions` must include that same
   private region. This is the load-bearing constraint and it is what makes
   the whole design safe and simple:
   - The SSH check's secrets are then **already sealed to that region's
     agents** (`checks.config_sealed`, age X25519 v2 envelope — spec
     2026-07-16-02). Dispatch ships that envelope **verbatim**; the executing
     agent is by construction one of its recipients and unseals it with its
     own identity. The server never decrypts, re-encrypts, or re-seals
     anything on this path — identical posture to the job's own
     `ConfigSealed` (`server/internal/agents/protocol.go:100-103`).
   - **Zero new credential exposure.** The only agents that ever receive the
     bastion envelope are agents of a region the SSH check is explicitly
     allocated to — exactly the set that can already unseal that check's own
     sealed config today.
   - The bastion gets monitored **from inside the private network** (the SSH
     check runs in that region too), which is the vantage that actually
     matters for its dependents.
   - Reseal-drift handling comes for free: the SSH check is a regular check
     of that region, so the existing reseal machinery
     (`credentials.NeedsReseal`, `agentws` reseal on connect —
     `server/internal/handlers/agentws/handler.go:170`) already covers its
     envelope. No tunnel-specific reseal code.
2. **Server-resolvability rule for cloud regions.** If the dependent check
   targets any **cloud** region, the referenced SSH check must be
   server-resolvable there — i.e. it must not be sealed-only. A sealed-only
   SSH check (allocated exclusively to private regions; `config_private`
   NULL, `models/check.go:77-84`) can only serve dependents that run
   exclusively in private regions it covers. Rejected at validation time with
   a message naming the fix, instead of today's guaranteed runtime
   `ErrNoCredentials`. The cloud dispatch path itself is unchanged
   (DB-backed resolver stays as wired in `server.go:321`).
3. **Transport: a `tunnel` block on the dispatched job, snapshotted at claim
   time.** `agents.AgentJob` gains an optional nested block carrying the SSH
   check's **public** config plus its `config_sealed` envelope verbatim.
   Nothing new is persisted — the block is assembled per claim in the
   `agentws` dispatch path from the live SSH check row. Edits to the SSH
   check are picked up on the next claim, mirroring how the job's own config
   behaves.
4. **Agent-side resolver via the existing seam.** Agent mode wires a
   resolver that resolves `(orgUID, tunnelCheckUID)` from the unsealed
   tunnel snapshots of currently-claimed jobs (in-memory only, held by
   `WSBackend`), then calls the existing `sshtunnel.Dial`. All
   dial/handshake/forward logic, host-key verification, failure
   classification (parent decision 10), and `tunnel_setup_ms` metric are
   reused unchanged. `WithResolver` remains the test seam.
5. **Same eligibility rules on both sides.** Server-side at validation and
   dispatch: referenced check exists in org, is type `ssh`, has
   `expected_fingerprint`, is not itself tunneled (no chaining). Agent-side
   after unseal: credentials present (username + password/private_key). The
   rule set is the parent spec's — no new rules, no relaxations.
6. **A job that cannot carry its tunnel is never dispatched half-armed and
   never silently skipped.** If at claim time the tunnel block cannot be
   built (SSH check deleted, region since removed, fingerprint cleared,
   chaining introduced), the job is dropped from the claim batch and an
   explicit `StatusError` result is written naming the fix — the same
   contract as a job whose sealed envelope cannot be opened (documented in
   `server/CLAUDE.md`, credential encryption section).
7. **Clearer terminal error.** An agent that receives a tunneled job with no
   tunnel block (version skew, or a race with decision 6) reports
   `"ssh tunnel: not available on this agent (the SSH check must be
   allocated to this agent's region)"` rather than the wiring-bug-flavored
   "resolver is not configured".

## Proposal

### 1. Validation (`server/internal/handlers/checks/tunnel.go`)

Extend `validateTunnelConfig` with the region rules (decisions 1–2):

- Every private region of the dependent must appear in the SSH check's
  `regions`.
- If the dependent has any cloud region, the SSH check must not be
  sealed-only.

Both fire on create and on update of the dependent (setting
`tunnelCheckUid` **or** changing `regions`). Error messages name the region
and the fix ("allocate SSH check `<slug>` to region `@paris` to use it as a
tunnel there").

Guard the SSH check's side too: `assertNotUsedAsTunnel` currently protects
delete (`ErrTunnelInUse`, `tunnel.go:13-17`); extend the same dependent scan
to **region-narrowing updates** of an SSH check — removing a private region
(or going sealed-only) while dependents rely on it is rejected with the
dependent labels in the message.

### 2. Dispatch (`server/internal/handlers/agentws/` + `server/internal/agents/protocol.go`)

- `AgentJob` gains `Tunnel *AgentJobTunnel` with
  `{ checkUid, config map[string]any, configSealed *string }`. `config` is
  the SSH check's **public** config only; `configSealed` is shipped
  verbatim (never `config_private` — same rule as `ToAgentJob` documents).
- In the claim path, for each claimed job whose config carries
  `tunnelCheckUid` (`checkerdef.TunnelCheckUIDFrom`), load the SSH check,
  re-assert the eligibility rules (decision 5), and attach the block.
  On failure: decision 6 (drop + `StatusError` result + lease release).

### 3. Agent side (`server/internal/checkworker/backend/ws.go` + `server/internal/agentmode/`)

- On claim, next to the existing job unseal (`ws.go:218-232`): if the job
  has a tunnel block, unseal `tunnel.configSealed` with the agent identity,
  merge with the public config (`credentials.MergeConfig`), build the
  `checkssh.SSHConfig`, and validate credential presence. Unseal failure is
  reported like the job-envelope unseal failure (clear "not sealed for this
  agent" result), not executed.
- Keep the plaintext snapshot in memory only, keyed by tunnel check UID,
  registered/unregistered with the job's lifecycle in `WSBackend`.
- Agent startup wires `sshtunnel.ResolverFunc` to a resolver that looks up
  the snapshot and calls `sshtunnel.Dial`. Missing snapshot → decision 7's
  error, wrapped as a tunnel `*Error` so classification holds.

### 4. Dashboard (`web/dash0` — start from the design reference per CLAUDE.md)

- The tunnel selector (shipped by spec 2026-07-18-01) filters/annotates SSH
  checks by region compatibility with the check being edited: incompatible
  ones are shown disabled with the reason ("not in region @paris").
- Surface the API validation errors inline on both the tunnel field and the
  regions field (a region change can now be rejected because of a tunnel
  dependency, in both directions).

### 5. Docs

- `web/docs` check-types / tunnel page: document the region rule ("to use a
  tunnel in a private region, allocate the SSH check to that region"), the
  sealed-only limitation for cloud dependents, and the error catalog
  (dispatch-time `StatusError` messages, agent-side unseal failures).

## Testing

- **Validation matrix** (`handlers/checks`): dependent private-only /
  cloud-only / mixed × SSH check region sets × sealed-only or not — assert
  accept/reject and message content. Region-narrowing update of an in-use
  SSH check rejected; delete guard still covered.
- **Protocol round-trip** (`internal/agents`): `AgentJob.Tunnel` survives
  `ToAgentJob`/`ToCheckJob`-equivalent mapping; `config_private` never
  crosses.
- **Dispatch guard** (`agentws` handler tests): tunneled job whose SSH check
  was deleted/de-regioned mid-flight → dropped from claim, `StatusError`
  result written, lease released; healthy path attaches the block.
- **Agent resolver** (`checkworker/backend`): unseal + resolve + dial against
  a test SSH server (the parent spec's test harness); wrong-identity unseal →
  clear failure result; missing block → decision 7 error, classified as
  tunnel failure.
- **End-to-end** (testcontainers): private-region check with
  `tunnelCheckUid` through an enrolled fake agent probes a target reachable
  only via the bastion; result up, `tunnel_setup_ms` present, duration
  excludes tunnel setup.
- **Playwright** (`web/dash0/e2e/`): selector disables out-of-region SSH
  checks; regions edit blocked with inline error when dependents exist.

## Follow-ups (not in scope)

- Uniform region rule for cloud regions too (dependent's regions ⊆ SSH
  check's regions everywhere) — would give same-vantage bastion monitoring
  but breaks existing cloud configs; needs a migration story.
- Tunnel support for the remaining tunnel-capable check types on agents
  follows automatically from this design (nothing here is type-specific).
- Agent-side connection pooling / multi-hop chaining — parent spec
  follow-ups, unchanged.

## Implementation Plan

### Step 1 — Validation rules (Proposal §1, decisions 1–2)

Region + sealed-only rules in `validateTunnelConfig`; region-narrowing guard
on the SSH check side. Unit tests (validation matrix).

### Step 2 — Protocol + dispatch (Proposal §2, decisions 3, 6)

`AgentJobTunnel` wire shape; claim-path attach with re-assertion; drop +
`StatusError` on failure. Protocol + handler tests.

### Step 3 — Agent unseal + resolver (Proposal §3, decisions 4–5, 7)

Unseal on claim, in-memory snapshot registry, `ResolverFunc` wiring in agent
mode, decision 7 error. Backend tests against test SSH server.

### Step 4 — End-to-end proof

Testcontainers E2E: enrolled agent, private-region tunneled check, bastion,
target. Assert result, metrics, and failure classification for the broken
variants.

### Step 5 — Dashboard + docs (Proposal §4–5)

Selector filtering, inline errors on tunnel/regions fields, docs page.
Playwright coverage.
