# Network discovery — scan local network for monitorable hosts

## Context

solidping today has no way to bootstrap a monitoring deployment beyond
manually adding checks one at a time. Users self-hosting in a LAN want
"point it at my network and tell me what's there." The label model
(`Label` + `CheckLabel` join, `server/internal/db/models/check.go:184-230`)
and the generic jobs framework (`server/internal/jobs/`, registry at
`server/internal/jobs/jobtypes/registry.go:8-31`) already give us everything
needed for an MVP except the scan engine itself, a candidate-host store,
and a small UI.

Three existing pieces shape the design:

- The generic `jobs` table (migration `024_fix_jobs_type_constraint.up.sql`)
  accepts any type matching `^[a-z][a-z0-9_-]{2,49}$`. `network_discovery`
  qualifies.
- `JobDefinition` / `JobRunner` (`server/internal/jobs/jobdef/interface.go:10-24`)
  is the extension point. Adding a new job type is: one constant in
  `jobdef/types.go`, one implementation under `jobtypes/`, one case in
  `registry.go:8-31`.
- `POST /api/v1/orgs/:org/jobs` (`server/internal/app/server.go:459`) is
  currently registered on the bare `api` group **without `RequireAuth`** —
  anyone reaching the endpoint can create a job today. Shipping a
  network-scan endpoint without fixing this would be reckless, so the
  auth fix is folded in.

## Goal

An admin user can trigger a discovery job from the dashboard, specifying
one or more CIDR ranges. The job (running in-process on the same machine
as the API server) probes each address with ICMP + TCP-connect on a small
port set, writes responsive hosts into a new `discovered_hosts` table
with suggested check types, and the user reviews the results and
selectively **promotes** candidates into real `Check` rows. Each promoted
check is tagged with the label `auto-discovery: true` and a
`discovery-job: <jobUid>` label so origin is traceable.

### Honest opinion (recorded at planning time)

**Auto-create vs candidates+promote.** Auto-creating a Check per
discovered host risks 100+ unwanted active checks from a single /24
scan. The candidates table is one extra migration but gives the user
control and a clean undo (just drop the row); the cost is justified.

**Pure Go vs nmap.** nmap is more powerful, but a runtime dependency on
a privileged native binary is the wrong default. For "find hosts to
monitor" we only need liveness + a hint at what's running. Pure Go
(`golang.org/x/net/icmp` for ICMP, `net.DialTimeout` for TCP) covers it
in <300 LOC and has no system requirements beyond ICMP capability on the
host. ICMP requires `CAP_NET_RAW` on Linux or root on macOS; when it's
unavailable we fall back to TCP-only liveness probing and log a warning
once per job.

**Worker routing.** Only a worker on the right LAN can scan that LAN.
The generic `jobsvc` (`service.go:433`, `claimNextJob`) has no
worker-context filtering — any worker grabs any job. Solving this
properly is its own design problem (would mirror the
`check_jobs.context_conditions` mechanism). For v1, discovery jobs run
**only** in the in-process worker; we document this in the spec and the
UI tooltip. A follow-up spec — *"Route jobsvc jobs by worker context"* —
will lift the limit.

## Non-goals

- nmap-style port-range scans, OS fingerprinting, service banners.
- Continuous / scheduled rediscovery. v1 is on-demand only.
- Auto-creating checks. The label `auto-discovery: true` is applied
  *at promotion time*, not at scan time.
- Worker-context routing in `jobsvc` (separate spec).
- Scanning IPv6 ranges. v1 is IPv4 / CIDRv4.
- Authenticated probes (SSH keys, HTTP basic auth) on discovered ports.

## Design

The work is naturally three vertical phases. Each is committable on its
own.

### Phase 1 — Backend: scan engine + job type + persistence

#### 1.1 New job type

- Add `JobTypeNetworkDiscovery JobType = "network_discovery"` to
  `server/internal/jobs/jobdef/types.go`.
- Register a `NetworkDiscoveryJobDefinition` in
  `server/internal/jobs/jobtypes/registry.go:8-31`.
- Implement in `server/internal/jobs/jobtypes/job_network_discovery.go`:

  ```go
  type NetworkDiscoveryConfig struct {
      CIDRs       []string `json:"cidrs"`                 // required, at least one
      Ports       []int    `json:"ports,omitempty"`       // optional override
      Concurrency int      `json:"concurrency,omitempty"` // default 64
      Timeout     string   `json:"timeout,omitempty"`     // per-probe, default "1s"
  }
  ```

  Default port set (when `Ports` is empty): `22, 25, 53, 80, 110, 143,
  443, 465, 587, 993, 995, 3306, 5432, 6379, 8080, 8443`.

#### 1.2 Scan engine

New package `server/internal/discovery/`:

- `scanner.go` — `Scan(ctx, config) ([]DiscoveredHost, error)`:
  expand CIDRs, semaphore for concurrency, ICMP echo first (skip on
  capability error, log once), then `net.DialTimeout("tcp", ...)`
  per port.
- `safety.go` — total addresses across all CIDRs must be ≤ 4096
  (a /20). Reject the job config at validate time with a clear
  error code `DISCOVERY_RANGE_TOO_LARGE`.
- `suggest.go` — port→suggested check type mapping (drives the
  promote UI):

  | Open port | Suggested check type | Config |
  |---|---|---|
  | ICMP up | `ping` | `{"host": "<ip>"}` |
  | 22 | `tcp` | `{"host": "<ip>", "port": 22}` |
  | 80 | `http` | `{"url": "http://<host>"}` |
  | 443 | `http` + `ssl` | `{"url": "https://<host>"}`, `{"host": "<host>"}` |
  | 25, 53, 110, 143, 465, 587, 993, 995, 3306, 5432, 6379, 8080, 8443 | `tcp` | port-specific |

- Hostname resolution via reverse DNS (`net.LookupAddr`), best-effort,
  bounded timeout per host.

#### 1.3 New table — `discovered_hosts`

Migration files:
- `server/internal/db/postgres/migrations/025_discovered_hosts.up.sql`
- `server/internal/db/postgres/migrations/025_discovered_hosts.down.sql`
- Mirror in `server/internal/db/sqlite/migrations/`.

Schema (PostgreSQL flavour):

```sql
CREATE TABLE discovered_hosts (
    uid                    UUID PRIMARY KEY,
    organization_uid       UUID NOT NULL REFERENCES organizations(uid),
    job_uid                UUID NOT NULL REFERENCES jobs(uid),
    ip                     INET NOT NULL,
    hostname               TEXT,
    open_ports             JSONB NOT NULL DEFAULT '[]'::jsonb,
    icmp_reachable         BOOLEAN NOT NULL DEFAULT FALSE,
    suggested_checks       JSONB NOT NULL DEFAULT '[]'::jsonb,
    promoted_to_check_uid  UUID REFERENCES checks(uid),
    discovered_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at             TIMESTAMPTZ
);
CREATE INDEX idx_discovered_hosts_org_job ON discovered_hosts (organization_uid, job_uid)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_active ON discovered_hosts (organization_uid, ip)
    WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;
```

The unique partial index prevents duplicate live candidates for the
same `(org, ip)` — a re-scan upserts.

#### 1.4 Model + repository

- `server/internal/db/models/discovered_host.go` — Bun model, mirrors
  the schema, includes `NewDiscoveredHost(orgUID, jobUID, ip)` helper.
- `server/internal/db/repo/discovered_hosts_repo.go` — `Upsert`,
  `ListByJob`, `ListPending` (deleted_at IS NULL AND
  promoted_to_check_uid IS NULL), `MarkPromoted`, `SoftDelete`.

#### 1.5 Concurrency / safety

One discovery job in-flight per org. The existing
`(type, config, orgUID, status=pending)` dedupe in `jobsvc/service.go:169`
is partial — we also need to reject if a `running` job of the same
type exists for the org. Add this check in `jobsvc.CreateJob` (or in
the discovery handler before delegation). Error code:
`DISCOVERY_ALREADY_RUNNING`.

### Phase 2 — Backend: API + auth fix

#### 2.1 Auth fix on `POST /api/v1/orgs/:org/jobs`

In `server/internal/app/server.go:457-459`, replace:

```go
jobHandler := jobs.NewHandler(s.jobSvc)
jobHandler.RegisterRoutes(api)
```

with a group that has `RequireAuth` + `RequireOrgAccess` applied. The
exact split between `/api/v1/jobs/*` (system-level, if any) and
`/api/v1/orgs/:org/jobs/*` (user-level) is verified during implementation
by reading `jobHandler.RegisterRoutes` — if every route is org-scoped,
the whole group goes behind `RequireAuth + RequireOrgAccess`. Admin-only
enforcement for `POST` happens inside the discovery handler (Phase 2.2),
not on the generic jobs handler, because non-admin users may need to
list/inspect their own jobs.

#### 2.2 New handler — `server/internal/handlers/discovery/`

`handler.go` + `service.go` + tests, following the pattern in
`server/CLAUDE.md` (HTTP in handler, business logic in service, no
DB access in handler).

Routes (all under `api.NewGroup("/orgs/:org/discovery").Use(RequireAuth, RequireOrgAccess)`):

| Method | Path | Role | Description |
|---|---|---|---|
| POST   | `/scans`              | admin | Start a new discovery job. Body: `{cidrs, ports?, timeout?, concurrency?}`. Returns the created `jobs` row. |
| GET    | `/scans`              | any   | List discovery jobs for this org, newest first. Wraps `jobs` list filtered by `type=network_discovery`. |
| GET    | `/scans/:jobUid`      | any   | One scan + status. |
| GET    | `/hosts`              | any   | List candidate hosts (`deleted_at IS NULL`). Filters: `jobUid`, `promoted` (bool). |
| POST   | `/hosts/:uid/promote` | admin | Body: `{checkType, name?, slug?, period?, extraConfig?}`. Creates a `Check` (reusing `checks.Service.CreateCheck`), attaches `auto-discovery: true` and `discovery-job: <jobUid>` labels, sets `promoted_to_check_uid`. Returns the new check. |
| DELETE | `/hosts/:uid`         | admin | Soft-delete the candidate (`deleted_at = now()`). |

Error codes: `VALIDATION_ERROR`, `DISCOVERY_RANGE_TOO_LARGE`,
`DISCOVERY_ALREADY_RUNNING`, `NOT_FOUND`, `FORBIDDEN`.

#### 2.3 Label deduplication

In the promote path, reuse `models.NewLabel` and `models.NewCheckLabel`
(`server/internal/db/models/check.go:193-230`). Look up an existing
`auto-discovery=true` label for the org (created once, reused on
subsequent promotions) — don't create a fresh Label row per promotion.
Same for `discovery-job=<jobUid>` (one per job). Wrap the whole promote
path in a transaction (create Check + attach labels + set
`promoted_to_check_uid`) to avoid orphans on mid-flight failure.

### Phase 3 — Frontend: discovery UI

#### 3.1 Routes

- `web/dash0/src/routes/orgs/$org/discovery.index.tsx` — list of past
  scans + "Start new scan" button.
- `web/dash0/src/routes/orgs/$org/discovery.new.tsx` — form:
  - `cidrs` (multi-line textarea, one per line)
  - "Advanced" expander: `ports`, `timeout`, `concurrency`
  - **Confirmation checkbox**: *"I confirm I own or have permission
    to scan the listed network(s)."* — disables submit when unchecked
  - Tooltip noting the in-process-worker-only limit
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx` — scan
  detail: job status + sortable list of candidate hosts, two row
  actions (`Pencil`-style "Promote" opens a sub-route;
  `Trash2` dismisses) per the dash0 row-action convention.
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.$hostUid.promote.tsx`
  — promote form, prefilled with `suggested_checks[0]`, allowing the
  user to switch check type / edit slug / name.

Model on `web/dash0/src/routes/orgs/$org/test.generate.tsx` (form →
mutation → toast → navigate to resource).

#### 3.2 Hooks — `web/dash0/src/api/hooks.ts`

Add: `useStartDiscoveryScan`, `useListDiscoveryScans`,
`useDiscoveryScan`, `useListCandidateHosts`, `usePromoteCandidate`,
`useDismissCandidate`. All scoped via `$org`, mirror existing TanStack
Query hook patterns.

#### 3.3 Navigation

Add a "Discovery" entry to the sidebar (confirm exact component path
during implementation — likely
`web/dash0/src/components/layout/sidebar.tsx`). Admin-only visibility.

#### 3.4 i18n

Strings via `useTranslation("discovery")`. New namespace files in
`web/dash0/public/locales/{en,fr}/discovery.json`. Register the
namespace in the i18n bootstrap (confirm path during implementation).

## Files to change

### New files (backend)

- `server/internal/jobs/jobtypes/job_network_discovery.go`
- `server/internal/discovery/scanner.go`
- `server/internal/discovery/safety.go`
- `server/internal/discovery/suggest.go`
- `server/internal/discovery/{scanner,safety,suggest}_test.go`
- `server/internal/db/models/discovered_host.go`
- `server/internal/db/repo/discovered_hosts_repo.go`
- `server/internal/db/repo/discovered_hosts_repo_test.go`
- `server/internal/db/postgres/migrations/025_discovered_hosts.up.sql`
- `server/internal/db/postgres/migrations/025_discovered_hosts.down.sql`
- `server/internal/db/sqlite/migrations/025_discovered_hosts.up.sql`
- `server/internal/db/sqlite/migrations/025_discovered_hosts.down.sql`
- `server/internal/handlers/discovery/handler.go`
- `server/internal/handlers/discovery/service.go`
- `server/internal/handlers/discovery/{handler,service}_test.go`

### Modified files (backend)

- `server/internal/jobs/jobdef/types.go` — add `JobTypeNetworkDiscovery`
- `server/internal/jobs/jobtypes/registry.go:8-31` — register the new type
- `server/internal/jobs/jobsvc/service.go:169` — extend dedup check to
  also reject when a `running` job of the same type exists for the org
- `server/internal/app/server.go:457-459` — wrap `jobHandler` routes
  behind `RequireAuth` + `RequireOrgAccess`; wire the new discovery handler

### New files (frontend)

- `web/dash0/src/routes/orgs/$org/discovery.index.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.new.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.$hostUid.promote.tsx`
- `web/dash0/public/locales/en/discovery.json`
- `web/dash0/public/locales/fr/discovery.json`
- `web/dash0/e2e/discovery.spec.ts`

### Modified files (frontend)

- `web/dash0/src/api/hooks.ts` — add the six discovery hooks
- sidebar component — admin-only "Discovery" entry (confirm path)
- i18n bootstrap file — register `discovery` namespace (confirm path)

## Verification

**Build, lint, test:**

```bash
make build
make lint
make test
```

**Migration round-trip (Postgres + SQLite):**

```bash
docker-compose up -d
make migrate
psql "$DATABASE_URL" -c '\d discovered_hosts'
./solidping migrate down 1 && ./solidping migrate up
```

**Auth fix regression:**

```bash
# Without token — must return 401 (was unauthenticated before)
curl -i -X POST -H 'Content-Type: application/json' \
  -d '{"type":"sleep","config":{}}' \
  http://localhost:4000/api/v1/orgs/default/jobs
# Expected: HTTP/1.1 401
```

**End-to-end happy path:**

```bash
make dev-test
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"test","email":"test@test.com","password":"test"}' \
  http://localhost:4000/api/v1/auth/login | jq -r '.accessToken')

# Start a tiny scan against localhost
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"cidrs":["127.0.0.1/32"],"ports":[80,443,4000]}' \
  http://localhost:4000/api/v1/orgs/test/discovery/scans | jq

# List discovered candidates
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:4000/api/v1/orgs/test/discovery/hosts | jq

# Promote first candidate
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"checkType":"tcp","name":"localhost-4000","slug":"localhost-4000"}' \
  http://localhost:4000/api/v1/orgs/test/discovery/hosts/<uid>/promote | jq

# Confirm auto-discovery label is attached
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/test/checks?label=auto-discovery:true' | jq
```

**Safety guard:**

```bash
# Expect 400 DISCOVERY_RANGE_TOO_LARGE
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"cidrs":["10.0.0.0/8"]}' \
  http://localhost:4000/api/v1/orgs/test/discovery/scans
```

**ICMP fallback:**

Run the e2e flow as an unprivileged user (no `CAP_NET_RAW`); confirm the
scan completes, the server log emits the "ICMP unavailable, TCP-only
liveness" warning exactly once, and TCP-reachable hosts still appear with
`icmp_reachable: false`.

**UI:**

```bash
make test-dash   # runs e2e/discovery.spec.ts
```

Plus manual: visit `/dash0/orgs/test/discovery`, start a scan against
`127.0.0.1/32`, promote a host, confirm it appears in `/checks` filtered
by label `auto-discovery=true`.

## Risk log

| Risk | Mitigation |
|---|---|
| ICMP needs raw-socket capability — common in self-hosted setups | Fall back to TCP-only liveness; log warning once per job; document in UI tooltip |
| User scans a large range and saturates their network | Hard cap at /20 (4096 addresses); default concurrency 64 + timeout 1s; reject larger ranges with `DISCOVERY_RANGE_TOO_LARGE` |
| Generic jobs auth gap broke a downstream consumer | Fix preserves route paths, only adds auth requirement; CI will catch any test relying on unauthenticated job creation (latent bug) |
| Multi-worker / SaaS deployments — wrong worker scans wrong LAN | v1 in-process-only; document clearly; follow-up spec adds worker-context routing to `jobsvc` |
| Duplicate candidate when same IP appears in two overlapping CIDRs | Unique partial index `idx_discovered_hosts_org_ip_active`; `Upsert` merges `open_ports` / `suggested_checks` on conflict |
| Promote path crashes mid-flight → orphan labels or half-created check | Wrap in transaction: create Check + attach labels + set `promoted_to_check_uid` atomically |
| Two admins start discovery concurrently | Reject second `CreateJob` call with `DISCOVERY_ALREADY_RUNNING`; UI hides "Start new scan" while a scan is `running` |
| Reverse DNS lookup delays the scan | Best-effort, bounded per-host timeout (~500ms), parallel to port probing, never blocking |
