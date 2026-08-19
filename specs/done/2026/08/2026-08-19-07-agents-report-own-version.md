---
model: sonnet
effort: high
---

# Agents never report their build version, so fleet version drift is invisible

## Problem

Deported agents and in-cluster checks workers run the same binary as the
server, but nothing on the wire ever says *which* version they are running.
Nowhere in the agent handshake or steady-state traffic is a build version
sent, and no `version` column exists on either `agents`
([agent.go:39](server/internal/db/models/agent.go:39)) or `workers`
([worker.go:14](server/internal/db/models/worker.go:14)). The only
version-ish field on the wire is `ServerFrame.Protocol`
([protocol.go:136](server/internal/agents/protocol.go:136)) — a protocol
number, server→agent only.

Concretely:

- The `enroll` frame carries only `Name` + public keys
  ([ws.go:615](server/internal/checkworker/backend/ws.go:615)); reconnects
  send only signed headers ([ws.go:695](server/internal/checkworker/backend/ws.go:695));
  the `claim` frame — the de-facto agent heartbeat — carries only
  `Capabilities` ([ws.go:245](server/internal/checkworker/backend/ws.go:245),
  [protocol.go:64](server/internal/agents/protocol.go:64)).
- Meanwhile the fleet genuinely drifts: the Fly.io agents pin an image tag
  per machine ([fly.toml:35](deploy/fly/fly.toml:35),
  [fly.nrt.toml:30](deploy/fly/fly.nrt.toml:30) — `0.7.1` today), the k8xp
  deployments each pin their own tag, and nothing reports back what is
  actually running. Identifying a stale agent today means shelling into
  every deployment target one by one.

The binary already knows its own version — `version.Get()`
([version.go:44](server/internal/version/version.go:44)), injected via
ldflags and served at `/api/mgmt/version`
([server.go:1916](server/internal/app/server.go:1916)) — and the agent runs
that same binary, so this is purely a wire/storage/surfacing gap, not a
build-plumbing one.

## Proposal

Follow the `capabilities` precedent (spec `2026-08-16-02`) exactly: an
optional self-reported field, nullable column, tri-state semantics,
rollout-order independent.

### Wire

- Add `Version string` (omitempty) to the `enroll` payload
  ([ws.go:615](server/internal/checkworker/backend/ws.go:615),
  [handler.go:218](server/internal/handlers/agentws/handler.go:218)) and to
  `ClientFrame` next to `Capabilities`
  ([protocol.go:64](server/internal/agents/protocol.go:64)), populated from
  `version.Get().Version` and sent on every `claim`.
- Older agents simply omit the field; older servers ignore the unknown JSON
  field. No protocol bump, no lockstep deploy.

### Storage

- Add a nullable `workers.version` column (migration `016_*` — latest on
  disk is `015_remove_opsgenie_integrations`; mirror the
  `capabilities` column recipe in
  [013_v0_16_0.up.sql:35](server/internal/db/postgres/migrations/013_v0_16_0.up.sql:35)
  including the SQLite triggers). `workers` is the right home: every
  deported agent already gets a worker row via `ensureWorkerRow`
  ([handler.go:399](server/internal/handlers/agentws/handler.go:399)), and
  the same column then covers in-cluster workers for free.
- Thread it through the existing heartbeat path:
  `UpdateWorkerHeartbeat(ctx, uid, capabilities)`
  ([service.go:209](server/internal/db/service.go:209), PG
  [postgres.go:1410](server/internal/db/postgres/postgres.go:1410), SQLite
  [sqlite.go:1363](server/internal/db/sqlite/sqlite.go:1363)) gains the
  version, as do `WorkerBackend.Heartbeat`
  ([backend.go:58](server/internal/checkworker/backend/backend.go:58)) and
  the in-cluster `registerWorker` / `updateHeartbeat`
  ([worker.go:348](server/internal/checkworker/worker.go:348),
  [worker.go:409](server/internal/checkworker/worker.go:409)). Agent-side,
  the server writes it from the throttled claim handler
  ([handler.go:448](server/internal/handlers/agentws/handler.go:448), same
  50s throttle as egress reports).

### Surfacing

- Add `version` to `AgentResponse`
  ([service.go:414](server/internal/handlers/agents/service.go:414)) —
  resolved from the agent's worker row (`agents.WorkerSlug(uid)`,
  [crypto.go:202](server/internal/agents/crypto.go:202)) — so it appears in
  `GET /api/v1/orgs/:org/agents` and `GET /api/v1/system/agents`; same for
  the workers listing if one exists.
- Dash0: add a Version column to the super-admin fleet table
  ([server.agents.tsx:69](web/dash0/src/routes/orgs/$org/server.agents.tsx:69))
  and to the per-org `AgentsCard`
  ([organization.private-locations.index.tsx:377](web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:377)).
  Compare against the server's own version from the existing `useVersion`
  hook ([hooks.ts:2742](web/dash0/src/api/hooks.ts:2742)): matching →
  plain text; differing → amber "drifted" badge; `null` → render
  **unknown**, never as drifted (tri-state discipline, exactly like
  capabilities — an old agent that predates this feature must not look
  broken).
- Server-side, log one WARN per connection when a reported version differs
  from the server's own — skip the comparison entirely when either side is
  a dev/untagged build (`version.Version` default is not a release tag).
- Update `wiki/api-specification/agents.md`, `wiki/database-model/agents.md`
  and the private-locations docs
  ([private-locations.md:140](web/docs/docs/features/private-locations.md:140)).

### Out of scope / open questions

- Automated remediation (auto-upgrade, blocking old agents) — this spec is
  detection only.
- A Prometheus metric (e.g. per-version gauge) would help alerting but can
  be a follow-up.
- If resolving the worker row from `AgentResponse` turns out awkward, a
  denormalized `agents.version` written from the same claim handler is an
  acceptable alternative — but prefer the single `workers.version` source
  of truth.

## Implementation Plan

Mirrors the `capabilities` precedent (spec `2026-08-16-02`) as closely as the
shapes allow. The one structural difference: `capabilities` is a set (three
states need NULL vs `{}` vs populated), `version` is a scalar (only two states
make sense for a single value — NULL/unknown, or a reported string — there is
no meaningful "reported but empty" state). The wire/UI tri-state described in
the spec (match / drifted / unknown) is a *comparison* result computed at
read time from that two-state column against the server's own version, not a
third stored state.

1. **Migration `016_worker_version` (scratch, both dialects, both
   directions).** Per `wiki/conventions/database.md`'s development workflow,
   013 is the last released migration and 014_v0_17_0 is the in-progress
   cycle's consolidated file, so new schema work before release is a new
   *feature-named* scratch file, not a version-named one — `016` because `015`
   (opsgenie removal) is already on disk. Adds `workers.version text`,
   nullable, no backfill, with a `CHECK (version is null or version <> '')`
   on both engines (a plain scalar CHECK, no SQLite triggers needed — those
   were for validating array *elements*, which does not apply to a single
   string). This is what turns "an agent that omits version" into a stored
   NULL rather than a stored `''` masquerading as a version.

2. **Model** (`internal/db/models/worker.go`). Add `Version *string
   \`bun:"version"\`` next to `Capabilities`, with a doc comment stating the
   two-state nullability (unlike `Capabilities`, no tri-state helper is
   needed here — the drift comparison lives in the read path, not the model).

3. **Wire** (`internal/agents/protocol.go`). Add `Version string
   \`json:"version,omitempty"\`` to `ClientFrame`, next to `Capabilities`.
   `omitempty` is correct here (unlike `Capabilities`): a real version is
   never the empty string, so there is no "I have none" answer to protect
   from collapsing into "not reported" the way there is for the capability
   set.

4. **Agent side** (`checkworker/backend/ws.go`). Populate `Version:
   version.Get().Version` on both the enroll frame (`dialEnroll`) and the
   claim frame (`claim`) — sent whenever the agent speaks, cheap, and gives
   the server a version to log even on an enroll-only connection.

5. **Server storage** (`handlers/agentws/handler.go`). Extend
   `recordAgentEgress` (the throttled, claim-driven refresh — same 50s
   window as capabilities) to also carry `frame.Version` through to
   `UpdateWorkerHeartbeat`. Empty string (never sent, or an old agent) means
   "not reported" and leaves the column untouched, exactly like a nil
   capability set. Add a `warnOnVersionDrift` helper, gated by a new
   `connState.versionChecked bool` so it fires **once per connection**
   regardless of the 50s throttle: skip entirely when either side is a
   dev/untagged build, else WARN once if the strings differ.

6. **Persistence** (`db/service.go`, `postgres.go`, `sqlite.go`).
   `UpdateWorkerHeartbeat` and `RegisterOrUpdateWorker` gain a `version
   string` parameter alongside `capabilities`, mirroring the "empty means
   don't touch" convention translated from nil-slice to empty-string (a real
   version is never empty, so the sentinel is safe). Both dialects stay
   byte-identical in structure to their capability-set siblings.

7. **In-cluster worker** (`checkworker/backend/backend.go`,
   `checkworker/backend/direct.go`, `checkworker/backend/ws.go`,
   `checkworker/worker.go`). `WorkerBackend.Heartbeat` and `.Register` thread
   the version through; `WSBackend`'s `Heartbeat` stays a no-op (version rides
   the claim frame, like capabilities). `registerWorker` sets
   `Worker.Version` at registration; `updateHeartbeat` re-sends it on every
   beat (cheap — `version.Get()` is a package-level constant lookup, not a
   probe).

8. **Surfacing** (`handlers/agents/service.go`). Add `Version *string
   \`json:"version"\`` to `AgentResponse` (no `omitempty` — the tri-state
   comparison downstream needs an explicit `null` for "unknown", not an
   absent key). Resolve it by fetching the full `ListWorkers` set once per
   `ListAgents`/`ListAllAgents` call and matching by
   `agentcrypto.WorkerSlug(agent.UID)`, rather than one `GetWorkerBySlug`
   call per agent — avoids N+1 queries on a fleet-wide listing.

9. **Dash0**: a new shared component,
   `web/dash0/src/components/shared/agent-version.tsx`, mirroring
   `ipv6-capability.tsx`'s structure (hardcoded English strings, no i18n —
   same as that component): `agentVersionState(agentVersion, serverVersion)`
   returns `"match" | "drifted" | "unknown"`, and `AgentVersionCell` renders
   plain text for match/unknown and an amber "Drifted" badge (with a tooltip
   naming the server's own version) for drifted. Wire it into
   `server.agents.tsx` (fleet table, compares against this server's own
   `useVersion()`) and `organization.private-locations.index.tsx`'s
   `AgentsCard` (same). Add both to `AgentInfo` (`api/hooks.ts`) as
   `version?: string | null`. Add an entry to `design-reference.tsx`
   alongside the existing IPv6 capability badge example.

10. **Docs.** `wiki/api-specification/agents.md` (the `version` field on the
    agent list responses), `wiki/database-model/checks.md` (the `workers`
    table gains a `version` column — the DB-model home for `workers`, cross
    -referenced from `wiki/database-model/agents.md` since the spec's
    `agents.md` API surface derives its `version` field from there),
    `wiki/features/deported-agents.md` (a short section mirroring the IPv6
    egress writeup), and `web/docs/docs/features/private-locations.md` (a
    short note near the environment-variables table).

11. **Tests** (D-bis): migration round-trip on both engines (NULL / value,
    CHECK rejects `''`); heartbeat "not reported" leaves a known version
    intact; an unknown extra field on the wire does not break decoding;
    tri-state rendering (`match`/`drifted`/`unknown`) with an explicit
    negative assertion that NULL renders as unknown, not drifted; the WARN
    fires once per connection, is skipped for dev/untagged builds on either
    side, and DOES fire for two differing release versions (positive
    control); end-to-end claim → worker row → `AgentResponse`.
