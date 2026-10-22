---
model: opus
effort: high
---

# LAN discovery cannot run from a private region's agent

Third of four specs for agent-run network discovery. Needs spec 01 (`discovered_checks.region`,
`OwnsPrivateRegion`) and spec 02 (`ICMPAvailable`). Spec 04 does the dashboard and docs.

## Problem
A LAN scan (`POST /orgs/:org/discovery/scans`, type `lan`) is a `network_discovery_plan` job that
fans out into `network_discovery` chunk jobs of at most 4096 addresses, executed by the server's
job worker (`server/internal/jobs/jobtypes/job_network_discovery_plan.go:84-103`,
`job_network_discovery.go:47-96`). It only sees what the server can route to. The networks
customers want inventoried (RFC1918 LANs, VPN-only segments) are the ones only a deported agent
(private region `@dc1`) reaches. In SaaS it is worse: LAN discovery is refused outright under the
enforcing egress guard (`server/internal/discovery/scantypes/lan.go:33-55`).

## Decisions
Taken 2026-10-21:
1. Discovery on an agent is authorised **only by the agent's operator**: `SP_AGENT_DISCOVERY_ENABLED`
   plus an agent-side CIDR allow-list `SP_AGENT_DISCOVERY_CIDRS`. No server-side toggle can widen it.
2. A promoted discovered check inherits the scan's private region (done in spec 01).
3. Stage 1 only: the agent runs liveness chunks (ICMP + TCP connect + reverse DNS, what
   `discovery.Scan` does today). Fingerprinting and L2 discovery (ARP, mDNS, SNMP) are later specs.

Design choices from the code review of 2026-10-21:
- **The agent pulls; the server never pushes.** A server→agent push from a chunk job cannot work.
  The agent socket registry is in-process, per replica (`handlers/agentws/uploads.go:20-26, 40-43`).
  Jobs run on any jobs-role node (`jobs/jobsvc/service.go:748-790`). A runner blocked waiting on an
  agent starves the shared queue (2 runners by default, `jobworker/worker.go:133-136`).
  `trace-request` is not a precedent: it is fire-and-forget with no reply on the socket
  (`agents/protocol.go:31-54`).
- **Over signed HTTP, not the WebSocket**, like `POST /agent/attachments` (`app/server.go:1479`).
  Any replica can serve it, the body cap is its own, and an old server answers 404. On the
  WebSocket, a frame over 256 KiB closes the whole agent connection
  (`handlers/agentws/handler.go:65-69`), and an unknown client frame type closes it too
  (`handler.go:994`). Check traffic would pay for discovery.
- **Chunks live in their own table**, not in `jobs`: they need an assigned agent, a lease and an
  exact `(org, region)` claim.
- **A new plan job type**, so an old node fails it with `unknown job type`
  (`jobworker/worker.go:313-320`). Reusing the existing types would make an old node drop the
  region and scan the range from the server's own network.
- **The agent returns raw facts only** (`ip, hostname, icmpReachable, openPorts`). The server
  checks them and recomputes suggestions with `discovery.SuggestChecks` (`suggest.go:60`). An agent
  must never author check configs (today `probeHost` builds them, `scanner.go:280-286`).
- **Threat model, stated honestly.** The allow-list bounds discovery, not the agent. Any member
  with role `user` can already pin a tcp/http check to `@dc1` on any host:port and run it now
  (`app/server.go:1232, 1510`), and trace requests take any address (`checkworker/backend/ws_trace.go:59-89`).
  Discovery adds breadth and speed, not reach. The allow-list is a breadth/noise control. Spec 04
  writes this in the docs.

## Proposal
1. **Agent config** (`server/internal/config/config.go`):
   - `AgentConfig` (`:196-232`) gets `DiscoveryEnabled bool` and `DiscoveryCIDRs []string`. Read
     them by hand in `applyAgentEnv` (`:2740-2771`), like `SP_AGENT_PRINT_KEYS`.
   - Add both names to `manualReaderEnvVars` (`envvars.go:216-222`).
   - Validate in `validateNodeConfig` (near `:3643-3647`):
     - enabled requires a non-empty list;
     - each entry parses with `netip.ParsePrefix`, is IPv4, and has no host bits;
     - entries overlapping `0.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16`, `224.0.0.0/4` or
       `240.0.0.0/4` are refused.
   - Agent-side caps, as constants in the agent (server-sent concurrency and timeout are ignored):
     concurrency 64, per-probe timeout 1 s, at most 32 ports, overall chunk deadline 10 min.
2. **Capability.**
   - Add `CapabilityDiscovery = "discovery"` next to `CapabilityBrowser` (`regions/capabilities.go:27-30`
     and its `models` source).
   - The agent appends it in `backend.Capabilities()` (`checkworker/backend/capabilities.go:18-41`)
     only when discovery is enabled **and** the egress set is non-nil. Appending to a nil set
     turns "unknown" into "no IPv4/IPv6".
   - Add it to `aggregatedCapabilities` (`regions/capabilities.go:91`) so `GET /regions` and
     `GET /private-regions` expose `capabilities.discovery`. A system agent's report must never
     count.
3. **Migration** (both engines, `.tx`). Same rule as spec 01: append to the same untagged migration file (`030_v0_41_0` on 2026-10-22), no new file. Table `discovery_chunks`:
   - columns: `uid`; `organization_uid` (fk, cascade); `plan_job_uid` (fk `jobs(uid)` on delete
     cascade, so it is cleaned with the plan by `job_jobs_cleanup.go`); `region text not null`;
     `cidrs` and `ports` (JSON text); `status` (`pending|claimed|done|failed|cancelled`, checked);
     `worker_uid` (nullable); `lease_until` (nullable); `attempts int not null default 0`;
     `error_code` (nullable); `host_count int not null default 0`; `created_at`; `updated_at`.
   - indexes on `(organization_uid, region, status, created_at)` and `(plan_job_uid)`.
   - add the model and the db methods on both engines.
4. **Scan start.**
   - `StartScanRequest` (`handlers/discovery/handler.go:86-89`) gets a top-level `region`. It is
     only allowed with type `lan`; any other type answers `422 VALIDATION_ERROR`.
   - Pass it to `BuildJob` (`discovery/scantypes/scantypes.go:102`, signature change), and inject
     the regions service into `scantypes.Deps` (`scantypes.go:63-73`, wiring at `app/server.go:1298-1301`).
   - In `LANDefinition.BuildJob` (`lan.go:50-88`), when a region is set:
     - require the canonical `@slug` (`ParsePrivateRegion`) and `OwnsPrivateRegion` (spec 01);
     - require at least one live, non-system agent of that region reporting `discovery`
       (`ListActiveAgentsByRegion` + `IsAgentLive` + worker capabilities,
       `regions/capabilities.go:187-217`), else `422 DISCOVERY_REGION_UNAVAILABLE`. This is a
       hint: the agent can still leave afterwards;
     - skip `LANNotAvailable` (`lan.go:53`);
     - return job type `network_discovery_agent_plan`, config `{cidrs, ports, region}`.
   - Map the new code in `statusForDiscoveryCode` (`handler.go:148-163`).
   - `GET /discovery/types` (`handler.go:66-83`, `service.go:118-132`) keeps `lan` under an
     enforcing guard when the org has at least one private region, flagged
     `serverAvailable: false` so the dashboard requires a region.
5. **Plan job.** New `jobs/jobtypes/job_network_discovery_agent_plan.go`, registered in
   `registry.go`:
   - `CreateJobRun` runs `ValidatePlanCIDRs` and requires a non-empty region;
   - `Run` calls `SplitCIDRs` (`discovery/safety.go:93`), bulk-inserts `pending` chunk rows and
     records `totalChunks`;
   - it never scans and never wraps a context in the server egress guard.
6. **Agent HTTP API.**
   - Registered next to `/agent/attachments`, authenticated with the same Ed25519 signed headers.
     Extract `attachments.Handler.authenticate` (`handlers/attachments/handler.go:248`) into a
     shared helper; do not copy it.
   - Refuse revoked agents (401) and system agents (403).
   - `POST /api/v1/agent/discovery/claim` → `200 {uid, cidrs, ports}` or `204`. In one atomic
     statement, take the oldest row where all of these hold:
     - org = the agent's org, and region = the agent's region exactly;
     - the plan job is not deleted;
     - `created_at` is within the last 24 h;
     - status is `pending`, or `claimed` with `lease_until < now`.

     Set `claimed`, `worker_uid`, `lease_until = now + 2 min` and `attempts + 1`. A row that would
     reach a 4th attempt becomes `failed` / `max_attempts` instead, and the next row is tried. An
     agent that already holds a live lease gets `204` (one chunk per agent). Postgres uses
     `FOR UPDATE SKIP LOCKED`; SQLite uses `UPDATE … WHERE uid = (SELECT … LIMIT 1) RETURNING`.
   - `POST /api/v1/agent/discovery/chunks/:uid/heartbeat` → `200` and extends the lease by 2 min.
     It answers `410` when the chunk is cancelled or not held by this worker.
   - `POST /api/v1/agent/discovery/chunks/:uid/result`, body capped at 1 MiB (`413` above). The body
     is `{hosts: [{ip, hostname, icmpReachable, openPorts}], partial, icmpAvailable, errorCode}`.
     - `410` when the chunk is not held by this worker, or is cancelled. Nothing is persisted.
     - Each host is kept only if its IP is IPv4, inside the chunk's CIDRs, and not a duplicate.
       More unique hosts than the chunk's address count rejects the whole body.
     - Ports outside the requested list are dropped.
     - A hostname longer than 253 characters or outside `[a-z0-9.-]` is emptied.
     - Suggestions are recomputed server-side, and any `suggestedChecks` field in the body is
       ignored. Rows are written with `UpsertDiscoveredChecks(…, region)` and `jobUID` = the plan
       UID.
     - The chunk becomes `done` with its `host_count`, or `failed` with `errorCode`. A partial
       result keeps its hosts but is `failed` / `partial`.
     - Before writing, check a cap of 20 000 active discovered checks per org. Above it, the chunk
       fails `inventory_full`. This keeps a dishonest agent from flooding the shared database.
   - Terminal codes sent by the agent: `DISCOVERY_NOT_ALLOWED`, `DISCOVERY_DISABLED`,
     `DISCOVERY_EGRESS_REFUSED`, `DISCOVERY_INVALID_CHUNK`.
7. **Agent side.** New `server/internal/checkworker/backend/discovery.go`, started from
   `agentmode/agentmode.go` (`:94-114`) only when enabled.
   - Loop: claim. On `204` or `404` (old server), sleep 15 s with jitter. On `200`:
     - **validate.** Parse every CIDR once with `netip.ParsePrefix`. Require `Is4()` (this rejects
       4-in-6) and `p == p.Masked()`. Require some allowed prefix `A` with
       `A.Bits() <= p.Bits() && A.Contains(p.Addr())`. Expand to a deduplicated address set whose
       size is at most `discovery.MaxAddresses`. Ports must be 1..65535, deduplicated, at most 32.
       Only the canonical strings that were checked are handed to `Scan`. Any failure → report
       `DISCOVERY_NOT_ALLOWED` or `DISCOVERY_INVALID_CHUNK`.
     - Refuse with `DISCOVERY_EGRESS_REFUSED` when the agent's guard is enforcing
       (`egress/egress.go:258-273`). Otherwise `Scan` would skip RFC1918 silently and report an
       empty success.
     - Run `Scan` under `egress.WithGuard(ctx, guard)`. The `WSBackend` guard is a struct field
       (`ws.go:90-92`) and `Scan` only reads the context. Apply the agent caps and a 10 min
       deadline. Heartbeat every 30 s and cancel on `410`.
     - Post the result without `SuggestedChecks` (add a `Scan` option to skip building them).
       `partial` is true on deadline or cancel; `icmpAvailable` comes from spec 02.
     - Claim again right away.
   - Logs: INFO per accepted chunk (uid, canonical CIDRs, port count, duration, host count) and
     WARN per refusal, never Debug. The agent has no DB, so this log is the only record a
     compromised server cannot rewrite.
8. **List, progress, cancel, guard** (`handlers/discovery/service.go`):
   - `ListScans` includes the new type.
   - `GetScan` reads progress for agent plans from `discovery_chunks`:
     - `completedChunks` = `done`;
     - `failedChunks` = `failed` plus `pending` rows older than 24 h (lazy expiry, no sweeper);
     - `runningChunks` = `claimed` with a live lease;
     - also `pendingChunks`, `region` and `lastErrorCode`.

     `derivedStatus` keeps today's semantics (`:414-428`).
   - `CancelScan` (`:447-494`) cancels the plan if it is pending, and sets `pending` / `claimed`
     chunks to `cancelled`. The heartbeat then answers 410 and a late result is discarded.
   - `checkScanAlreadyRunning` (`:225-262`) counts an agent plan as running while it has a
     non-expired `pending` chunk or a live `claimed` one. It stays per org.
9. **Audit** with `audit.Record` (`audit/audit.go:258-285`). Register the new types in
   `db/models/event.go:289-298`.
   - `discovery.scan_started` in `StartScan`, for every scan. Payload `{scan_type, region,
     cidr_count, cidrs (first 32), address_count, port_count}`.
   - `discovery.scan_canceled` in `CancelScan`.
   - `discovery.chunk_refused` (system actor, target the agent) when an agent reports a terminal
     refusal, at most once per `(plan, agent)`.
   - No payload key may contain `private`, `key`, `token` or `auth`: redaction drops them silently
     (`audit/redact.go:34-48`).
10. **Admin gate.** For region-bound scans, check the role on the membership row (the
    `orgRoleAtLeast` pattern, `handlers/checks/handler.go:183-197`), not `claims.Role`, which stays
    valid up to 1 h after a demotion (`handlers/discovery/handler.go:27-34`). See open question 1.
11. **OpenAPI** `server/internal/app/openapi/openapi.yaml` (`:5413-5495`, `:16341`): the
    `region` field, `DISCOVERY_REGION_UNAVAILABLE`, `serverAvailable`.

## Tests
- `config` tests:
  - enabled with an empty list fails;
  - an IPv6 entry fails;
  - `10.0.0.5/24` (host bits) fails;
  - `127.0.0.0/8` and `0.0.0.0/0` fail;
  - the env vars are recognised (`envvars_test.go:209-240`).
- `checkworker/backend/discovery_test.go`. The allow-list is passed to the handler directly, so
  `127.0.0.1/32` is usable even though config refuses it. Use `countingListener` from
  `discovery/scanner_egress_test.go:20-47`.
  - positive control: with `127.0.0.1/32` allowed, `accepted > 0`;
  - a chunk outside the list → `DISCOVERY_NOT_ALLOWED` and `accepted == 0`;
  - `10.0.0.5/20` against `10.0.0.0/24`, and `::ffff:10.0.0.0/116`, are both refused;
  - 4096 copies of `127.0.0.1/32` count as 1 address and dial once per port;
  - discovery disabled → the loop never claims;
  - an enforcing guard → `DISCOVERY_EGRESS_REFUSED`;
  - heartbeat `410` cancels the scan;
  - the deadline gives `partial: true`;
  - the result body has no suggested checks.
- Agent API tests, both engines:
  - a claim returns only the agent's org and exact region;
  - two orgs both define `@dc1`, each with an agent: neither claims the other's chunks;
  - a result from org A's agent for org B's chunk → 410 and no rows written;
  - a system agent → 403;
  - a second claim while a lease is held → 204;
  - an expired lease is reclaimable by another agent, attempts increment, and the 4th attempt
    becomes `failed` / `max_attempts`;
  - result validation: an IP outside the chunk, an unrequested port and a bad hostname are all
    dropped, and more hosts than addresses → 400;
  - a result for a cancelled chunk → 410 and nothing persisted;
  - a body over 1 MiB → 413;
  - a `suggestedChecks` field in the body is ignored, and stored suggestions equal
    `SuggestChecks(ip, hostname, icmp, ports)`;
  - the org inventory cap → `inventory_full`.
- `job_network_discovery_agent_plan_test.go`:
  - N chunk rows are created with the region;
  - nothing is dialed (positive control: a `network_discovery` chunk on the same CIDR does dial).
- `handlers/discovery` tests:
  - enforcing guard plus region is accepted;
  - enforcing guard without region still gets `403 DISCOVERY_NOT_AVAILABLE` (next to
    `TestNetworkDiscoveryPlanRefusedUnderEgressPolicy`);
  - region on a non-lan type → 422;
  - a foreign region, a cloud slug and `@acmetech/dc1` → 422;
  - no live discovery agent → `422 DISCOVERY_REGION_UNAVAILABLE`;
  - a demoted admin with a still-valid token → 403;
  - `discovery.scan_started` carries the actor and the region;
  - `GetScan` progress and `lastErrorCode` for an agent plan;
  - `CancelScan` cancels the chunks;
  - the already-running guard sees an agent plan and ignores expired chunks.
- `regions` tests: `capabilities.discovery` is aggregated from org agents only, never from a
  system agent.
- `make test-postgres` for the claim SQL.

## To verify
- The db layer shape for a new table (interface in `db/service.go`, sqlite and postgres
  implementations) and whether bulk insert exists.
- Whether the region capability aggregation (`regions/capabilities.go:187-217`) can see
  `IsSystem()` to exclude system agents, or needs a join.
- Whether `/agent/attachments` is in `openapi.yaml`. Document the agent discovery endpoints the
  same way, or leave them out the same way.
- The name of the egress package's reserved-prefix table, to reuse it for the config refusal list.

## Open questions
1. May super admins, and impersonation sessions, launch scans from a customer's agent?
   **Recommended: no.** A region-bound scan requires an admin membership row in the org; a super
   admin without one gets 403. `POST /api/v1/orgs/{org}/discovery/scans` joins
   `impersonationForbiddenRoutes` (`handlers/auth/impersonation_guard.go:48-89`), with a test
   that impersonation gets `IMPERSONATION_FORBIDDEN`.
2. SaaS gets LAN discovery for the first time, and today nothing bounds how much a tenant can
   scan through its free agent (4096 chunks per scan, back to back). Limit it?
   **Recommended: yes**, a new entitlement `discovery_agent_addresses_per_day` (SaaS default
   65 536, one /16 a day; unlimited self-hosted), checked at scan start with
   `422 DISCOVERY_QUOTA_EXCEEDED` (`entitlements/defaults.go:77-84`).
