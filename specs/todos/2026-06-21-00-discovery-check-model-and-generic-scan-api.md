# Discovery rework — check-centric grouped model + generic typed scan API

> **Foundation spec for the discovery source family.** It refactors the *existing*
> LAN + Freebox discovery onto a check-centric, grouped data model and a single
> generic `{type, parameters}` scan API, so that adding a new source
> (container-discovery `…-01`, kubernetes-discovery `…-03`) becomes "register a
> discovery type" — no schema change, no new endpoint. **This spec must land
> first**; `…-01` and `…-03` depend on it. The `kubernetes` checker (`…-02`) is
> independent of this spec.

## Context

Discovery today is **host-centric**. A scan writes one `discovered_hosts` row per
IP, and each row carries an embedded `suggested_checks` JSON array:

- `server/internal/db/models/discovered_host.go` — `DiscoveredHost` (struct
  `:24-37`): `ip INET NOT NULL`, `hostname`, `open_ports`, `icmp_reachable`,
  `suggested_checks jsonb`, `source` (enum `:16-21`, `lan`/`freebox`), `job_uid`,
  `promoted_to_check_uid`. Unique identity is the IP:
  `idx_discovered_hosts_org_ip_source_active` on `(organization_uid, ip, source)`
  (Postgres `migrations/001_v0_1_0.up.sql:1001-1002`, SQLite `:775-776`).
- Two **per-type** start endpoints + handlers:
  `POST /api/v1/orgs/:org/discovery/scans` (`handler.go:118-154` → `StartScan`,
  body is `disc.Config`) and `POST …/discovery/freebox-scans`
  (`handler.go:74-116` → `StartFreeboxScan`, body `{channelUid}`).
- **Chunking** is LAN-specific and lives in the job layer: a
  `network_discovery_plan` job (`job_network_discovery_plan.go:50`) splits the
  CIDRs with `disc.SplitCIDRs(cidrs, disc.MaxAddresses)` (`safety.go:93`,
  `MaxAddresses=4096`) into ≤`MaxScanChunks=4096` child `network_discovery` jobs
  (`:77-96`), each rolling its hosts up under the plan UID
  (`job_network_discovery.go:60-62`). Freebox is a single
  `freebox_lan_discovery` job, no chunking.
- Progress is surfaced by `GET /scans/:jobUid` via `ScanProgress`
  (`service.go:267-275`: `totalChunks`, `completed/failed/running/pending`,
  `derivedStatus`, `hostCount`), computed only for the plan type
  (`GetScanProgress`, `:281-348`).
- The frontend lists scans (`discovery.index.tsx`), then renders a **flat
  per-host table** (`discovery.$jobUid.index.tsx`, `HostRow :57-125`), and the
  promote page (`discovery.$jobUid.$hostUid.promote.tsx`) shows the host's
  `suggested_checks` as checkboxes.

This shape does not extend. Container and Kubernetes discovery each need a stable
non-IP identity (many containers/workloads behind one host/API-server IP), forcing
bolt-on identity columns and a reworked IP unique index onto an IP-keyed table; and
each new source multiplies the per-type endpoints, request DTOs, service methods,
and frontend branches. Suggested checks — the actual deliverable of discovery — are
second-class, buried as a JSON blob on a host row.

## The reframing

**A discovered thing is a *suggested check*, and suggested checks are *grouped* for
display.** A LAN host with ports 80/443 open is not one "host" — it is a *group*
(`192.168.1.5`, or its hostname) containing three suggested checks (HTTP, HTTPS,
ICMP). A container is a group (`web-1`) containing a `docker` check plus published-
port checks. A workload is a group (`prod/api`) containing a `kubernetes` check plus
reachable-endpoint checks. The group is purely a **rendering** concern; the stored
unit is the check.

This dissolves the identity problem (there is no `ip` column to collide on — `ip`
becomes a group label), makes suggested checks first-class, and unifies promotion
("create these checks") across every source.

## Goal

1. Replace `discovered_hosts` with **`discovered_checks`** — one row per suggested
   check `{group_key, group_label, name, slug, type, config}` plus a `source` and
   optional group `metadata`.
2. Replace the per-type endpoints with **one generic** `POST /discovery/scans` body
   `{type, parameters}`, backed by a **discovery-type registry** mirroring the
   checker registry — each type declares its parameter validation and the job it
   enqueues.
3. Keep the **existing chunking unchanged** (LAN plan/child fan-out, `SplitCIDRs`,
   4096) and surface it **uniformly** through `ScanProgress` for *every* scan type
   (a single chunk for non-chunked types).
4. Frontend: a generic registry-driven scan-start form, **grouped** rendering of
   discovered checks, and **per-check promotion with group multi-select**.
5. **Clean break**: drop `discovered_hosts` and both per-type endpoints; no data
   migration (discovery results are regenerable — re-scan). Migrate LAN + Freebox
   onto the new model as the reference implementation.

## Non-goals

- New discovery sources (`container`, `kubernetes`) — separate specs (`…-01`,
  `…-03`) that register a type against this foundation.
- Changing the LAN chunking engine, limits, or safety caps (`SplitCIDRs`,
  `MaxAddresses=4096`, `MaxScanChunks=4096`, `ValidatePlanCIDRs`) — unchanged.
- Data migration from `discovered_hosts` — the table is dropped; rows are
  regenerable by re-scanning.
- Continuous/scheduled re-discovery or keeping checks in sync — on-demand only, as
  today.
- Auto-creating checks — promotion stays explicit; the `auto-discovery` /
  `discovery-job` labels are applied at promote time, as today.

## Design

Six independently-committable slices. Slices 1–4 are backend, 5 is frontend, 6 is
the removal of the old surface.

### 1. Model + DB — `discovered_checks`

New model `server/internal/db/models/discovered_check.go` (replaces
`discovered_host.go`). Keep the `DiscoverySource` enum (`lan`, `freebox`; `container`
and `kubernetes` are added by their own specs):

```go
type DiscoveredCheck struct {
    bun.BaseModel `bun:"table:discovered_checks"`

    UID              string  `bun:"uid,pk"                json:"uid"`
    OrganizationUID  string  `bun:"organization_uid"      json:"organizationUid"`
    JobUID           string  `bun:"job_uid"               json:"jobUid"`        // the scan (plan UID for chunked)
    Source           DiscoverySource `bun:"source"        json:"source"`        // lan|freebox|container|kubernetes

    GroupKey   string `bun:"group_key"   json:"groupKey"`   // stable grouping identity (IP, container ID, workload uid)
    GroupLabel string `bun:"group_label" json:"groupLabel"` // display: hostname-or-IP, container name, "ns/name"

    Name   string          `bun:"name"   json:"name"`   // suggested check name (discovery-generated)
    Slug   string          `bun:"slug"   json:"slug"`   // suggested check slug (discovery-generated, unique per group)
    Type   string          `bun:"type"   json:"type"`   // check type: http|tcp|icmp|docker|kubernetes…
    Config json.RawMessage `bun:"config,type:jsonb" json:"config"` // the check config

    Metadata json.RawMessage `bun:"metadata,type:jsonb" json:"metadata,omitempty"` // group-display hints (denormalized)

    PromotedToCheckUID *string    `bun:"promoted_to_check_uid" json:"promotedToCheckUid,omitempty"`
    DiscoveredAt       time.Time  `bun:"discovered_at"         json:"discoveredAt"`
    CreatedAt          time.Time  `bun:"created_at"            json:"createdAt"`
    UpdatedAt          time.Time  `bun:"updated_at"            json:"updatedAt"`
    DeletedAt          *time.Time `bun:"deleted_at,soft_delete" json:"deletedAt,omitempty"`
}

// NewDiscoveredCheck builds a row from a SuggestedCheck within a group.
func NewDiscoveredCheck(orgUID, jobUID string, source DiscoverySource,
    groupKey, groupLabel string, sc SuggestedCheck, meta json.RawMessage) *DiscoveredCheck { … }
```

`metadata` is **denormalized** group-display state (e.g. LAN `{openPorts,
icmpReachable}`; container `{image,state,healthStatus,dockerHost}`; k8s
`{kind,desiredReplicas,readyReplicas,conditions}`) — identical across rows of one
group, written by the suggester. There is **no separate groups table**: grouping is
a render-time `GROUP BY group_key`.

Migration **`003_discovery_checks.{up,down}.sql`** (next free number — current dirs
hold `001_v0_1_0` and `002_mcp_oauth`; one consolidated migration per release per
`server/CLAUDE.md`), Postgres + SQLite mirror:

```sql
-- up (Postgres; SQLite mirror: uuid→text, jsonb→text, timestamptz→datetime, now()→current_timestamp)
DROP TABLE IF EXISTS discovered_hosts;   -- clean break; rows are regenerable by re-scanning

CREATE TABLE discovered_checks (
  uid                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  organization_uid      uuid        NOT NULL REFERENCES organizations(uid),
  job_uid               uuid        NOT NULL,
  source                text        NOT NULL,
  group_key             text        NOT NULL,
  group_label           text        NOT NULL,
  name                  text        NOT NULL,
  slug                  text        NOT NULL,
  type                  text        NOT NULL,
  config                jsonb       NOT NULL DEFAULT '{}',
  metadata              jsonb,
  promoted_to_check_uid uuid        REFERENCES checks(uid),
  discovered_at         timestamptz NOT NULL DEFAULT now(),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  deleted_at            timestamptz
);

-- One row per (group, slug) per source while active & unpromoted → upsert key.
CREATE UNIQUE INDEX idx_discovered_checks_identity_active
  ON discovered_checks (organization_uid, source, group_key, slug)
  WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;

-- List/group by scan, and source-filter.
CREATE INDEX idx_discovered_checks_job        ON discovered_checks (job_uid)               WHERE deleted_at IS NULL;
CREATE INDEX idx_discovered_checks_org_source ON discovered_checks (organization_uid, source) WHERE deleted_at IS NULL;
```

`.down.sql` drops `discovered_checks` and recreates `discovered_hosts` per its
original `001` definition (empty). Neither `source` nor any reused column has a DB
`CHECK` constraint (verified: `discovered_hosts.source` is `text not null default
'lan'`), so new source values are Go-level only.

### 2. Discovery-type registry

New package `server/internal/discovery/scantypes/` (registry mirrors
`checkers/registry`). A discovery type owns its parameter schema, validation, and
the job it enqueues — the handler/service stay type-agnostic.

```go
// Definition is one discovery source/method.
type Definition interface {
    Type() string                       // "lan", "freebox", "container", "kubernetes"
    Source() models.DiscoverySource     // the row `source` it writes
    // BuildJob validates `parameters` and returns the job to enqueue. For chunked
    // types it returns the PLAN job type (fan-out happens inside that job's Run,
    // unchanged). A validation failure returns a coded error
    // (DISCOVERY_INVALID_PARAMETERS, or a type-specific code via DiscoveryError).
    BuildJob(ctx context.Context, deps Deps, orgUID string,
        parameters json.RawMessage) (jobType string, jobConfig json.RawMessage, err error)
}

func Register(d Definition)            // called from each type's init/registry wiring
func Get(typ string) (Definition, bool)
func List() []Definition               // for the frontend capability list
```

- **`lan`** `BuildJob`: validate `{cidrs, ports?, concurrency?, timeout?}` via
  `disc.ValidatePlanCIDRs` → `("network_discovery_plan", cfg)`. Chunking unchanged.
- **`freebox`** `BuildJob`: validate `{channelUid}` resolves to a granted channel →
  `("freebox_lan_discovery", cfg)`; `FREEBOX_NOT_GRANTED` on failure.
- (`container`/`kubernetes` register their own definitions in their specs.)

`Service.StartScan(ctx, orgUID, typ string, parameters json.RawMessage)` replaces
`StartScan`/`StartFreeboxScan`: `Get(typ)` (→ `DISCOVERY_UNKNOWN_TYPE`), `BuildJob`,
`checkAlreadyRunning(orgUID, jobType)` (reusing `service.go:192-263`, now keyed on
the resolved job type → `DISCOVERY_ALREADY_RUNNING`), `jobSvc.CreateJob`, return the
scan job. The job-layer registry (`jobtypes/registry.go`) is unchanged in shape; new
sources still register a `JobDefinition` there *and* a `scantypes.Definition` here.

### 3. Suggested checks → grouped rows

`SuggestedCheck` (`suggest.go:12-15`) gains the display fields so a suggester emits
fully-formed rows:

```go
type SuggestedCheck struct {
    GroupKey   string          `json:"groupKey"`
    GroupLabel string          `json:"groupLabel"`
    Name       string          `json:"name"`
    Slug       string          `json:"slug"`
    Type       string          `json:"type"`
    Config     json.RawMessage `json:"config"`
    Metadata   json.RawMessage `json:"metadata,omitempty"` // group hints (denormalized)
}
```

Shared helpers in `discovery/suggest.go` keep names/slugs consistent across sources:
`checkName(groupLabel, scheme)` (e.g. `"192.168.1.5 · HTTP"`) and
`checkSlug(groupLabel, scheme, port)` (e.g. `"192-168-1-5-http"`, deduped within a
group). Refactor the existing **LAN** `SuggestChecks` (`suggest.go:18`) to return, per
host, one `SuggestedCheck` per open-port mapping (via `defaultPorts`, `ports.go:20-37`)
plus an ICMP check when reachable — all sharing `groupKey = ip`, `groupLabel =
hostname||ip`, `metadata = {openPorts, icmpReachable}`. Keep `checkType*` constants
(`suggest.go:6-9`).

Persistence: replace `UpsertDiscoveredHost` and the two `persistHosts`
(`job_network_discovery.go:85-136`, `job_freebox_lan_discovery.go:120-162`) with a
shared `UpsertDiscoveredChecks(ctx, db, orgUID, jobUID, source, rows)` that upserts on
`(organization_uid, source, group_key, slug)` (Postgres `ON CONFLICT … DO UPDATE`,
SQLite mirror) — preserving the **per-item log-and-continue** resilience contract
(never abort a run on one bad row). The LAN child still rolls rows up under the plan
UID (`job_network_discovery.go:60-62`).

### 4. API

**Scan lifecycle (generic):**
- `POST /api/v1/orgs/:org/discovery/scans` (admin-only, `isAdmin` `handler.go:33-40`):
  body `{ "type": "lan", "parameters": { … } }`. Decode → `Service.StartScan`.
  Returns the scan job (`DiscoveryScan`) + its `ScanProgress`.
- `GET /scans`, `GET /scans/:jobUid`, `POST /scans/:jobUid/cancel` — **kept,
  generalized** to any registered scan job type (the child-filtering in `ListScans`
  `handler.go:157-192` keys on the plan/child relationship, not on `lan` specifically;
  drive the user-facing-type set from `scantypes.List()` so new types appear without
  edits). `ScanProgress` (`service.go:267-275`) is returned for **every** scan: chunked
  types compute it from children as today; non-chunked types report `totalChunks=1`
  and the single job's status. Rename `hostCount` → expose both `groupCount` (distinct
  `group_key`) and `checkCount` (rows) from `discovered_checks`.

**Discovered checks (replaces `/hosts`):**
- `GET /discovery/checks?jobUid=&source=&group=&promoted=` → `{ "data":
  [DiscoveredCheck…] }`. The frontend groups by `groupKey`; an optional `group=`
  narrows to one group.
- `POST /discovery/checks/promote` body `{ "uids": ["…","…"], "overrides": { … } }`
  → creates one real check per referenced discovered-check row (config/name/slug
  already on the row; `overrides` may adjust `period`/`name` etc.), tags them
  `auto-discovery: true` + `discovery-job: <jobUid>`, sets `promoted_to_check_uid`,
  returns `{ "data": [Check…] }`. One UID promotes a single check; a group's UIDs
  promote the whole group (the frontend's "select all in group"). `normalizeCheckType`
  (`service.go:659`, only remaps `ping→icmp`) still applies; `docker`/`kubernetes`
  pass through unchanged.
- `DELETE /discovery/checks/:uid` → dismiss one row (soft delete). Optional
  `DELETE /discovery/checks?jobUid=&group=` to dismiss a whole group.

**Error codes:** add `DISCOVERY_UNKNOWN_TYPE`, `DISCOVERY_INVALID_PARAMETERS`;
keep `DISCOVERY_ALREADY_RUNNING`, `FREEBOX_NOT_GRANTED`. Document the generic scan
route + the `discovered_checks` shape in `server/internal/app/openapi/openapi.yaml`.

### 5. Frontend (`web/dash0/`)

*All UI reuses the design-reference primitives per `CLAUDE.md`; start from
`design-reference.tsx`.*

- **`discovery.new.tsx`** — replace the hard-coded `ScanMethod` union (`:38`) and
  `Select` (`:164-178`) with a registry-driven list (a `DISCOVERY_TYPES` descriptor
  per type: label, capability gate, and a parameter sub-form component). LAN renders
  the CIDR/advanced form; Freebox the channel `Select`. Submit dispatches the single
  `useStartDiscoveryScan({ type, parameters })`.
- **`discovery.index.tsx`** — scan list; source filter from the registry
  (`scanSource :51-53`, filter `:136-148`).
- **`discovery.$jobUid.index.tsx`** — render discovered checks **grouped by
  `groupLabel`**: a group header (label + source badge + `metadata` badges: ports/ICMP
  for LAN) with its suggested checks beneath (`name`, `type`, config hint). Each check
  has a checkbox; the group header has **"select all in group"**; a **Promote
  selected** action posts the selected UIDs; per-check and per-group **dismiss**
  (`Trash2`, destructive). Replaces the flat `HostRow` table (`:57-125`).
- **Promote** — selection happens inline on the grouped list; a lightweight confirm
  step (name/period overrides) posts to `/discovery/checks/promote`. The standalone
  `…/$hostUid/promote.tsx` route is removed.
- **`web/dash0/src/api/hooks.ts`** — new types `DiscoveredCheck`, `DiscoveryScan`
  (with `progress`), `StartDiscoveryScanRequest { type; parameters }`; hooks
  `useStartDiscoveryScan`, `useListDiscoveredChecks`, `usePromoteChecks`,
  `useDismissCheck`. **Remove** `useStartFreeboxScan`, `useListCandidateHosts`,
  `usePromoteCandidate`, `useDismissCandidate`, and `DiscoveredHost`/`SuggestedCheck`
  host types (`:3224-3402`). `canSource`/`CAPABILITIES` (`:2742-2764`) drive which
  types appear.
- **i18n** — restructure `web/dash0/src/locales/{en,fr,de,es}/discovery.json` to
  check/group vocabulary: keep `method*`/`source*`; add `group`, `selectAllInGroup`,
  `promoteSelected`, `suggestedCheckName`, etc.

### 6. Remove the old surface

Delete `discovered_host.go`, `UpsertDiscoveredHost`, the host DTOs, the `/hosts`
handlers + `StartFreeboxScan`/old `StartScan`, and the host-centric tests. Update all
references. `git grep -n discovered_host` and `DiscoveredHost` must come back empty.

## Decisions

1. **Check-centric, single table** with denormalized group `metadata` — grouping is
   a render-time concern (`group_key`), not a second table.
2. **Generic `{type, parameters}` scan API + discovery-type registry**, the
   checker-registry pattern. One endpoint, one service method, N registered types.
3. **Clean break** — drop `discovered_hosts` and both per-type endpoints; no data
   migration (results are regenerable). LAN+Freebox are migrated onto the new model
   as the reference implementation in this spec.
4. **Chunking unchanged** — LAN's plan/child fan-out and caps are untouched; the API
   surfaces chunk progress **uniformly** for every type (`totalChunks=1` when not
   chunked).
5. **Per-check promotion with group multi-select** — each row is independently
   promotable; the group view promotes any subset (or all) in one `promote` POST.
6. **Suggesters generate `name`+`slug`** per check so promotion is "create these
   checks" (editable at promote), not "pick from a host's suggestions".

## Files to create / modify

### New (backend)
- `server/internal/db/models/discovered_check.go` (+ test) — replaces `discovered_host.go`.
- `server/internal/discovery/scantypes/{registry.go,lan.go,freebox.go}` (+ tests) —
  the type registry and the two reference definitions.
- Migration `003_discovery_checks.{up,down}.sql` (Postgres + SQLite).

### Modified (backend)
- `server/internal/discovery/suggest.go` — `SuggestedCheck` display fields,
  `checkName`/`checkSlug` helpers, LAN `SuggestChecks` emits grouped rows.
- `server/internal/discovery/scanner.go` — `Config` parameters used by the `lan`
  definition.
- Persistence: shared `UpsertDiscoveredChecks`; rework `job_network_discovery.go`,
  `job_network_discovery_plan.go`, `job_freebox_lan_discovery.go` to use it.
- `server/internal/handlers/discovery/{handler,service}.go` (+ tests) — generic
  `POST /scans`, `Service.StartScan(type,params)`, generalized `ListScans`/`GetScan`/
  `CancelScan` + `ScanProgress` (`groupCount`/`checkCount`), `/checks` list/promote/
  dismiss, new error codes.
- `server/internal/app/openapi/openapi.yaml` — generic scan route + `discovered_checks`.

### Removed (backend)
- `discovered_host.go`, `UpsertDiscoveredHost`, `/hosts` handlers, `StartFreeboxScan`,
  old `StartScan` signature, host DTOs/tests.

### Frontend
- `discovery.new.tsx` (registry-driven form), `discovery.index.tsx`,
  `discovery.$jobUid.index.tsx` (grouped render); **remove**
  `discovery.$jobUid.$hostUid.promote.tsx`.
- `web/dash0/src/api/hooks.ts` — new check/scan types + hooks; remove host hooks.
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`.
- `web/dash0/e2e/discovery.spec.ts` — grouped render + per-check/group promote.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()`):**
  - LAN `SuggestChecks` → grouped rows: a host with 80/443 open + ICMP reachable
    yields HTTP/HTTPS/ICMP rows sharing one `group_key`, distinct `slug`s, correct
    `metadata`. Slug/name dedup within a group.
  - `scantypes`: `lan`/`freebox` `BuildJob` validate parameters and return the right
    `(jobType, config)`; unknown type → `DISCOVERY_UNKNOWN_TYPE`; bad params →
    `DISCOVERY_INVALID_PARAMETERS`; freebox ungranted → `FREEBOX_NOT_GRANTED`.
  - `UpsertDiscoveredChecks`: upsert on `(org, source, group_key, slug)`; re-scan
    updates in place; one bad row logs+continues.
  - `ScanProgress` uniform: a Freebox scan reports `totalChunks=1`; a LAN plan rolls
    up children; `groupCount`/`checkCount` from `discovered_checks`.
  - Promote: multiple UIDs → multiple checks, each labelled `auto-discovery`;
    `ping→icmp` remap preserved; `promoted_to_check_uid` set.
- **Migration round-trip** (Postgres + SQLite): apply + `migrate down 1` + up;
  confirm `discovered_checks` indexes exist and `discovered_hosts` is gone (up) /
  back (down).
- **End-to-end** (`make dev-test`): run a real LAN scan through `POST /scans
  {type:"lan",…}`; confirm hosts render **grouped** with per-port checks; select a
  group and promote; confirm the created checks carry `auto-discovery: true` and run.
  Repeat for a Freebox scan (single chunk, progress shows 1/1).
- **Guards:** unknown type → 400 `DISCOVERY_UNKNOWN_TYPE`; bad params → 400
  `DISCOVERY_INVALID_PARAMETERS`; second concurrent scan of the same type → 409
  `DISCOVERY_ALREADY_RUNNING`; non-admin → 403.
- `make build lint test test-dash` green; `git grep -n DiscoveredHost` empty.

## Risk log

| Risk | Mitigation |
|---|---|
| Clean break drops in-flight `discovered_hosts` rows | Results are regenerable; a re-scan repopulates. No external API consumers (pre-1.0, in-repo frontend). |
| Group metadata is denormalized across a group's rows | Acceptable for an ephemeral scratch table; written once per group by the suggester; rendering reads any row of the group. |
| Generalizing `ListScans`/`ScanProgress` could regress LAN progress | Drive the user-facing-type set from `scantypes.List()`; keep the plan/child rollup math identical; cover Freebox (1 chunk) + LAN (N chunks) in tests. |
| Two registries (job-layer + scantypes) per source could drift | A source registers in both; an `activation`-style test asserts every `scantypes` type has a matching `JobDefinition`. |
| Slug collisions within a group (e.g. two ports → same scheme) | `checkSlug` appends the port/index to disambiguate; the unique index is `(org, source, group_key, slug)`. |
| Frontend grouped render + inline promote is a larger UI change than a flat table | Reuse design-reference primitives; the grouped list is the only genuinely new component (group header + select-all); promote becomes a thin confirm. |
| `container`/`kubernetes` specs assume this landed | This spec is ordered first (`…-00`); `…-01`/`…-03` state the dependency explicitly. |

**Status**: Todo | **Created**: 2026-06-27
