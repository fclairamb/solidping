---
model: opus
effort: high
---

# LAN discovery cannot run from a private region's agent

Part of the agent-run work series (see 2026-10-22-02). Needs 2026-10-21-01
(`discovered_checks.region`, `OwnsPrivateRegion`), 2026-10-21-02 (`ICMPAvailable`) and
2026-10-22-02 (`worker_tasks`). Spec 2026-10-22-05 does the dashboard and docs.

## Problem
A LAN scan (`POST /orgs/:org/discovery/scans`, type `lan`) is a `network_discovery_plan` job. It
fans out into `network_discovery` chunk jobs of at most 4096 addresses, executed by the server's
job worker (`server/internal/jobs/jobtypes/job_network_discovery_plan.go:84-103`,
`job_network_discovery.go:47-96`). It only sees what the server can route to.

The networks customers want inventoried (RFC1918 LANs, VPN-only segments) are the ones only a
deported agent (private region `@dc1`) reaches. In SaaS it is worse: LAN discovery is refused
outright under the enforcing egress guard (`server/internal/discovery/scantypes/lan.go:33-55`).

## Decisions
Taken 2026-10-21:
1. Discovery on an agent is authorised **only by the agent's operator**:
   `SP_AGENT_DISCOVERY_ENABLED` plus an agent-side CIDR allow-list `SP_AGENT_DISCOVERY_CIDRS`.
   No server-side toggle can widen it.
2. A promoted discovered check inherits the scan's private region (done in 2026-10-21-01).
3. Stage 1 only: the agent runs liveness chunks (ICMP + TCP connect + reverse DNS, what
   `discovery.Scan` does today). Fingerprinting and L2 discovery (ARP, mDNS, SNMP) are later
   specs.

Taken 2026-10-22: a chunk is a `worker_tasks` row of kind `discovery` (one slice, no state),
claimed over the generic agent task routes of 2026-10-22-02. There is no `discovery_chunks`
table and no discovery-specific HTTP route. Server-run LAN discovery keeps its `jobs` chunks.

Design choices from the code review of 2026-10-21:
- **A new plan job type**, so an old node fails it with `unknown job type`
  (`jobworker/worker.go:313-320`). Reusing the existing types would make an old node drop the
  region and scan the range from the server's own network.
- **The agent returns raw facts only** (`ip, hostname, icmpReachable, openPorts`). The server
  checks them and recomputes suggestions with `discovery.SuggestChecks` (`suggest.go:60`). An
  agent must never author check configs (today `probeHost` builds them, `scanner.go:280-286`).
- **Threat model, stated honestly.** The allow-list bounds discovery, not the agent.
  - Any member with role `user` can already pin a tcp/http check to `@dc1` on any host:port and
    run it now (`app/server.go:1232, 1510`).
  - Trace requests take any address (`checkworker/backend/ws_trace.go:59-89`).
  - So discovery adds breadth and speed, not reach. The allow-list is a breadth/noise control.
    Spec 05 writes this in the docs.

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
   - Agent-side caps, as constants in the executor (server-sent concurrency and timeout are
     ignored): concurrency 64, per-probe timeout 1 s, at most 32 ports, overall chunk deadline
     10 min.
2. **Executor** `discovery` (capability `discovery`), registered with the task loop of
   2026-10-22-02 only when discovery is enabled. Add `CapabilityDiscovery` next to
   `CapabilityBrowser` (`regions/capabilities.go:27-30` and its `models` source).
3. **Scan start.**
   - `StartScanRequest` (`handlers/discovery/handler.go:86-89`) gets a top-level `region`. It is
     only allowed with type `lan`; any other type answers `422 VALIDATION_ERROR`.
   - Pass it to `BuildJob` (`discovery/scantypes/scantypes.go:102`, signature change), and inject
     the regions service into `scantypes.Deps` (`scantypes.go:63-73`, wiring at
     `app/server.go:1298-1301`).
   - In `LANDefinition.BuildJob` (`lan.go:50-88`), when a region is set:
     - require the canonical `@slug` (`ParsePrivateRegion`) and `OwnsPrivateRegion`;
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
4. **Plan job.** New `jobs/jobtypes/job_network_discovery_agent_plan.go`, registered in
   `registry.go`:
   - `CreateJobRun` runs `ValidatePlanCIDRs` and requires a non-empty region;
   - `Run` calls `SplitCIDRs` (`discovery/safety.go:93`), bulk-inserts `pending` `discovery`
     tasks (`job_uid` = the plan, payload `{cidrs, ports}`, the scan's region) and records
     `totalChunks`;
   - it never scans and never wraps a context in the server egress guard.
5. **Kind `discovery`, server side** (`TaskKind`):
   - `Complete`, with body `{hosts: [{ip, hostname, icmpReachable, openPorts}], partial,
     icmpAvailable, errorCode}`:
     - each host is kept only if its IP is IPv4, inside the task's CIDRs, and not a duplicate.
       More unique hosts than the task's address count rejects the whole body;
     - ports outside the requested list are dropped;
     - a hostname longer than 253 characters or outside `[a-z0-9.-]` is emptied;
     - suggestions are recomputed server-side, and any `suggestedChecks` field in the body is
       ignored;
     - rows are written with `UpsertDiscoveredChecks(…, region)` and `jobUID` = the plan UID;
     - the task becomes `done` with `progress.hostCount`, or `failed` with `errorCode`. A partial
       result keeps its hosts but is `failed` / `partial`;
     - before writing, check a cap of 20 000 active discovered checks per org. Above it, the task
       fails `inventory_full`. This keeps a dishonest agent from flooding the shared database.
   - Terminal codes sent by the agent: `DISCOVERY_NOT_ALLOWED`, `DISCOVERY_DISABLED`,
     `DISCOVERY_EGRESS_REFUSED`, `DISCOVERY_INVALID_CHUNK`.
6. **Kind `discovery`, agent side** (`checkworker/backend/discovery.go`):
   - **Validate:**
     - parse every CIDR once with `netip.ParsePrefix`, and require `Is4()` (this rejects 4-in-6)
       and `p == p.Masked()`;
     - require some allowed prefix `A` with `A.Bits() <= p.Bits() && A.Contains(p.Addr())`;
     - expand to a deduplicated address set of at most `discovery.MaxAddresses`;
     - ports must be 1..65535, deduplicated, at most 32;
     - only the canonical strings that were checked are handed to `Scan`;
     - any failure → `finish` with `DISCOVERY_NOT_ALLOWED` or `DISCOVERY_INVALID_CHUNK`.
   - Refuse with `DISCOVERY_EGRESS_REFUSED` when the agent's guard is enforcing
     (`egress/egress.go:258-273`). Otherwise `Scan` would skip RFC1918 silently and report an
     empty success.
   - Run `Scan` under the task loop's egress context, with the agent caps and a 10 min deadline.
   - `finish` without `SuggestedChecks` (add a `Scan` option to skip building them). `partial`
     is true on deadline or cancel; `icmpAvailable` comes from 2026-10-21-02.
   - Logs: INFO per accepted task (canonical CIDRs, port count, host count), WARN per refusal.
7. **List, progress, cancel, guard** (`handlers/discovery/service.go`):
   - `ListScans` includes the new type.
   - `GetScan` reads progress for agent plans from `worker_tasks` (`job_uid` = the plan):
     - `completedChunks` = `done`;
     - `failedChunks` = `failed` plus `pending` rows older than 24 h;
     - `runningChunks` = `claimed` with a live lease;
     - also `pendingChunks`, `region` and `lastErrorCode`.

     `derivedStatus` keeps today's semantics (`:414-428`).
   - `CancelScan` (`:447-494`) cancels the plan if it is pending, and its tasks
     (`workertasks.Cancel`). The heartbeat then answers 410 and a late result is discarded.
   - `checkScanAlreadyRunning` (`:225-262`) counts an agent plan as running while it has a
     non-expired `pending` task or a live `claimed` one. It stays per org.
8. **Audit** with `audit.Record` (`audit/audit.go:258-285`). Register the new types in
   `db/models/event.go:289-298`.
   - `discovery.scan_started` in `StartScan`, for every scan. Payload `{scan_type, region,
     cidr_count, cidrs (first 32), address_count, port_count}`.
   - `discovery.scan_canceled` in `CancelScan`.
   - `discovery.chunk_refused` (system actor, target the agent) when an agent reports a terminal
     refusal, at most once per `(plan, agent)`.
   - No payload key may contain `private`, `key`, `token` or `auth`: redaction drops them
     silently (`audit/redact.go:34-48`).
9. **Admin gate.** For region-bound scans, check the role on the membership row (the
   `orgRoleAtLeast` pattern, `handlers/checks/handler.go:183-197`), not `claims.Role`, which
   stays valid up to 1 h after a demotion (`handlers/discovery/handler.go:27-34`). See open
   question 1.
10. **OpenAPI** `server/internal/app/openapi/openapi.yaml` (`:5413-5495`, `:16341`): the
    `region` field, `DISCOVERY_REGION_UNAVAILABLE`, `serverAvailable`.

## Tests
- `config` tests:
  - enabled with an empty list fails;
  - an IPv6 entry fails;
  - `10.0.0.5/24` (host bits) fails;
  - `127.0.0.0/8` and `0.0.0.0/0` fail;
  - the env vars are recognised (`envvars_test.go:209-240`).
- `checkworker/backend/discovery_test.go`. The allow-list is passed to the executor directly, so
  `127.0.0.1/32` is usable even though config refuses it. Use `countingListener` from
  `discovery/scanner_egress_test.go:20-47`.
  - positive control: with `127.0.0.1/32` allowed, `accepted > 0`;
  - a chunk outside the list → `DISCOVERY_NOT_ALLOWED` and `accepted == 0`;
  - `10.0.0.5/20` against `10.0.0.0/24`, and `::ffff:10.0.0.0/116`, are both refused;
  - 4096 copies of `127.0.0.1/32` count as 1 address and dial once per port;
  - discovery disabled → the executor is not registered, and the capability is absent;
  - an enforcing guard → `DISCOVERY_EGRESS_REFUSED`;
  - a cancelled context gives `partial: true`;
  - the result body has no suggested checks.
- Kind `discovery`, both engines:
  - an IP outside the task, an unrequested port and a bad hostname are all dropped;
  - more hosts than addresses → rejected;
  - a `suggestedChecks` field in the body is ignored, and stored suggestions equal
    `SuggestChecks(ip, hostname, icmp, ports)`;
  - rows carry the task's region;
  - the org inventory cap → `inventory_full`.
- `job_network_discovery_agent_plan_test.go`:
  - N tasks are created with the region;
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
  - `CancelScan` cancels the tasks;
  - the already-running guard sees an agent plan and ignores expired tasks.
- `make test-postgres`.

## To verify
- The name of the egress package's reserved-prefix table, to reuse it for the config refusal list.

## Resolved open questions
Decided by Florent on 2026-10-22:
1. Super admins and impersonation sessions may not launch scans from a customer's agent. A
   region-bound scan requires an admin membership row in the org; a super admin without one gets
   403. `POST /api/v1/orgs/{org}/discovery/scans` joins `impersonationForbiddenRoutes`
   (`handlers/auth/impersonation_guard.go:48-89`), with a test that impersonation gets
   `IMPERSONATION_FORBIDDEN`.
2. Agent discovery is bounded by a new entitlement `discovery_agent_addresses_per_day` (SaaS
   default 65 536, one /16 a day; unlimited self-hosted), checked at scan start with
   `422 DISCOVERY_QUOTA_EXCEEDED` (`entitlements/defaults.go:77-84`). Tests: a scan over the
   remaining daily quota is refused, a self-hosted org is never refused.
