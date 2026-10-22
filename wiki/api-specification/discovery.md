# Network Discovery

On-demand discovery of monitorable endpoints. Findings land in
`discovered_checks` and can be listed, promoted into real checks, or dismissed.
All routes are under `/api/v1/orgs/:org/discovery` and require auth + org
access; mutating routes additionally require **admin**.

The noun is the **discovered check**, not a host — the `discovered_hosts` table
was dropped in migration 002 and there are no `/discovery/hosts` routes.
Freebox LAN listing is not part of this API either; it lives at
`GET /api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts` (see
[integrations.md](integrations.md)).

## Types

### GET /api/v1/orgs/:org/discovery/types
List the registered discovery types and their capability descriptors
(`{type, source, serverAvailable}`), driving the frontend type picker.
`serverAvailable` is `false` when the server itself may not run the type (`lan`
under an enforcing egress policy): a scan then needs a private region. Auth:
required

## Scans

### POST /api/v1/orgs/:org/discovery/scans
Launch a discovery scan. Auth: admin. The body is `{type, parameters, region?}`: `type` is a registered type, `parameters` its type-specific object, and `region` (`@slug`, a private region of the org) runs a `lan` scan from that region's agents instead of the server. A region on any other type answers `422 VALIDATION_ERROR`. A region-bound scan needs an admin membership row (no super-admin bypass), is refused under impersonation (`IMPERSONATION_FORBIDDEN`), and answers `422 DISCOVERY_REGION_UNAVAILABLE` when no live agent of the region runs discovery, or `422 DISCOVERY_QUOTA_EXCEEDED` when the org's `maxDiscoveryAgentAddressesPerDay` (per UTC day, summed from agent plans) is spent. A server LAN scan and an agent scan cannot run at once in an org (`409 DISCOVERY_ALREADY_RUNNING`). Large ranges are accepted: the scan is created as a `network_discovery_plan` job (its UID is the scan UID) that fans out into bounded `network_discovery` child jobs of ≤4096 addresses each, so hosts appear progressively. Returns `{ "data": <job> }`. `422 DISCOVERY_RANGE_TOO_LARGE` if the range exceeds the overall ceiling (`MaxScanChunks` = 256 chunks ≈ 1M addresses / a /12). `409 DISCOVERY_ALREADY_RUNNING` if a scan is already in flight for the org (a plan or any non-stale child pending/running; children whose `updated_at` is older than 30m are ignored).

### GET /api/v1/orgs/:org/discovery/scans
List discovery runs, newest first. Child `network_discovery` jobs (those carrying a `parentJobUid` in config) are filtered out — the plan job represents the scan. Auth: required. Returns `{ "data": [<job>, ...] }`.

### GET /api/v1/orgs/:org/discovery/scans/:jobUid
Get one discovery run. Auth: required. For a `network_discovery_plan` scan the response also carries a `progress` block: `{ "totalChunks", "completedChunks", "failedChunks", "runningChunks", "pendingChunks", "derivedStatus", "groupCount", "checkCount", "region"?, "lastErrorCode"? }`. `region` is set for an agent-run scan, `lastErrorCode` is the error code of its latest failed chunk (`DISCOVERY_NOT_ALLOWED`, `DISCOVERY_DISABLED`, `DISCOVERY_EGRESS_REFUSED`, `DISCOVERY_INVALID_CHUNK`). `derivedStatus` is `running` while the plan is pending/running or any child is pending/running, `success` once all children are terminal, `failed` only if the plan itself failed.

### POST /api/v1/orgs/:org/discovery/scans/:jobUid/cancel
Stop a running fan-out scan. Auth: admin. Cancels the plan job if still pending, then soft-deletes every pending child chunk; children already running are interrupted (a chunk running on an agent stops at the agent's next heartbeat, 30 s at most). Returns `204 No Content`. `404 NOT_FOUND` if no such scan exists for the org.

Agent-run chunks are `worker_tasks` rows of kind `discovery`: see
[worker-tasks.md](../database-model/worker-tasks.md). Audit events:
`discovery.scan_started`, `discovery.scan_canceled`, `discovery.chunk_refused`.

## Discovered checks

### GET /api/v1/orgs/:org/discovery/checks
List the discovered checks for the org. Auth: required. Returns
`{ "data": [<discoveredCheck>, ...] }`. Each row carries a `source`
discriminator identifying which discovery type produced it, and a `region`
(omitted when the server itself found it, `@slug` when a private region did).

### POST /api/v1/orgs/:org/discovery/checks/promote
Promote discovered checks into real checks. Auth: admin. **Bulk and
body-driven** — the selection and the per-item options are in the request body,
not the path; there is no per-uid promote route. A row with a `region` creates a
check pinned to that region (`regions: ["@slug"]`). If the org no longer owns the
region the whole call is refused with `422 DISCOVERY_REGION_UNAVAILABLE` and
nothing is created. Rows without a region keep the default placement.

### DELETE /api/v1/orgs/:org/discovery/checks/:uid
Dismiss (soft-delete) a single discovered check. Auth: admin.

### DELETE /api/v1/orgs/:org/discovery/checks
Dismiss a whole group of discovered checks in one call. Auth: admin. Query: `group` (required), `jobUid` (optional), `region` (optional, default `""` = found by the server itself): only rows of that region are dismissed.
