# Discovered-host `source` — unify LAN + Freebox discovery sources

## Context

`discovered_hosts` (migration `027`, model `server/internal/db/models/discovered_host.go`)
is written by exactly one path today: the `network_discovery` job's
`persistHosts` (`server/internal/jobs/jobtypes/job_network_discovery.go:84`). A
near-identical upsert also exists in `server/internal/handlers/discovery/service.go:129`
(`UpsertDiscoveredHost`), currently unused by the job path.

Freebox LAN discovery ([`2026-05-24-08-freebox-lan-discovery.md`](2026-05-24-08-freebox-lan-discovery.md)) is read-only: the
channels handler `LanHostsHandler` (`server/internal/handlers/channels/freebox_lan.go:34`)
proxies `freebox.ListLanHosts` live and never persists. So there is no way to track,
list, promote, or dismiss a Freebox-found host the way LAN-scan hosts are tracked.

This spec adds a `source` discriminator to `discovered_hosts` and makes Freebox a
first-class discovery source that persists into the same table via a new job type,
so both sources share the existing list / promote / dismiss UX.

### Honest opinion (recorded at planning time)

This is a consolidation + quality-of-life feature, not load-bearing. The only
genuinely tricky bit is the **unique index**: today it's `(organization_uid, ip)`
among active, unpromoted rows. If LAN and Freebox both see `192.168.1.10`, the
second upsert would clobber the first and flip its source. We widen the key to
`(organization_uid, ip, source)` so the two sources coexist as separate rows — the
honest model ("this IP, found via this source"). Cost: the same device can appear
twice in a cross-source view. That's acceptable and clearer than silently merging.

The Freebox job is a single synchronous API call wrapped in the async job machinery
purely for consistency with `network_discovery` (per the chosen mechanism). It is
cheap; no scan engine, no CIDR expansion.

## Goal

- Add a `source` column to `discovered_hosts` (`'lan'` | `'freebox'`, extensible),
  backfilling existing rows to `'lan'`.
- Widen the active-host unique index to include `source`.
- A new `freebox_lan_discovery` job that queries a paired Freebox channel and
  upserts its LAN hosts as `source='freebox'`.
- An admin endpoint to launch a Freebox discovery run; the run shows up in the
  existing scans list and drills into the existing host table.
- Frontend: a **Source** badge/column on the host table and scans list, plus a
  source filter, and a "Discover via Freebox" launcher on the discovery page.

## Non-goals

- Continuous/scheduled rediscovery (both sources stay on-demand).
- Port scanning for Freebox hosts (Freebox gives us name + reachability only).
- A brand-new "all hosts across all jobs" page — filtering is added to the existing
  scans list and the `/hosts` endpoint; the per-job host view is unchanged in shape.
- Touching `web/dash` (legacy dashboard).

## Data model

New typed enum + field in `server/internal/db/models/discovered_host.go`, following
the `models.ProviderType` / `jobdef.JobType` convention (named string + constants):

```go
type DiscoverySource string

const (
    DiscoverySourceLAN     DiscoverySource = "lan"
    DiscoverySourceFreebox DiscoverySource = "freebox"
)
```

Add the field and update the constructor signature:

```go
Source DiscoverySource `bun:"source,notnull" json:"source"`
// ...
func NewDiscoveredHost(orgUID, jobUID, ip string, source DiscoverySource) *DiscoveredHost
```

### Migration `030_discovered_hosts_source` (both backends)

`server/internal/db/{sqlite,postgres}/migrations/030_discovered_hosts_source.up.sql`:

```sql
-- NOT NULL DEFAULT 'lan' backfills all existing rows automatically
ALTER TABLE discovered_hosts ADD COLUMN source TEXT NOT NULL DEFAULT 'lan';

-- widen the active-host uniqueness to be per-source
DROP INDEX idx_discovered_hosts_org_ip_active;
CREATE UNIQUE INDEX idx_discovered_hosts_org_ip_source_active
    ON discovered_hosts (organization_uid, ip, source)
    WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL;
```

`...down.sql` reverses it: drop the new index, recreate
`idx_discovered_hosts_org_ip_active` on `(organization_uid, ip)`, then
`ALTER TABLE discovered_hosts DROP COLUMN source` (SQLite ≥3.35 supports DROP COLUMN).
Highest existing migration is `029` in both dirs, so `030` is correct.

### Upsert conflict target

Both upserts must reference the new index:

- `server/internal/jobs/jobtypes/job_network_discovery.go:111`
- `server/internal/handlers/discovery/service.go:148` (Postgres) and `:159` (SQLite)

```
CONFLICT (organization_uid, ip, source) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE
```

`source` is part of the conflict key and is never updated on conflict. The
network-discovery `persistHosts` passes `models.DiscoverySourceLAN` to
`NewDiscoveredHost`.

## Backend — `freebox_lan_discovery` job

### Job type registration

- Add `JobTypeFreeboxLanDiscovery JobType = "freebox_lan_discovery"` to
  `server/internal/jobs/jobdef/types.go`.
- Add the `case` to `server/internal/jobs/jobtypes/registry.go`.

### New job — `server/internal/jobs/jobtypes/job_freebox_lan_discovery.go`

Config shape: `{ "channelUid": "<freebox channel uid>" }`.

`Run`:
1. Require `jctx.OrganizationUID`; unmarshal `channelUid` from config.
2. Call `freebox.ListLanHostsForChannel(ctx, jctx.DBService, jctx.Services.Credentials, orgUID, channelUID)` (see shared resolver below).
3. Map each `freebox.LanHost` → `models.DiscoveredHost`:
   - `ip` ← `LanHost.IP`, `hostname` ← `LanHost.Name`,
     `icmp_reachable` ← `LanHost.Reachable`, `open_ports` ← `[]`,
     `suggested_checks` ← `[]` (the promote flow's `buildCheckConfig` falls back
     to `config["host"] = host.IP` when the list is empty, so promotion works
     without explicit suggestions), `source = DiscoverySourceFreebox`.
4. Upsert each host using an inline upsert mirroring `persistHosts` in
   `job_network_discovery.go`. Non-fatal per-host on error, like `persistHosts`.

### Shared Freebox LAN resolver (avoid a layering inversion)

`channels.Service.ListFreeboxLanHosts` (`freebox_lan.go:63-160`) owns the
load-channel → decrypt-token → build-client → `freebox.ListLanHosts` chain, but
`jobtypes` must not import `handlers/channels`.

Extract that chain into a handler-independent function that depends only on
`db.Service` + `credentials.Service` + the `freebox` package.
New file: `server/internal/integrations/freebox/lanlookup.go`:

```go
func ListLanHostsForChannel(
    ctx context.Context,
    dbSvc db.Service,
    creds credentials.Service,
    orgUID, channelUID string,
) ([]LanHost, error)
```

It resolves the channel by UID, verifies `conn.OrganizationUID == orgUID` and type
Freebox, decrypts the app token (replicating the `resolveFreeboxAppToken` logic),
builds the client via `freebox.NewClientWithAppID`, and calls `freebox.ListLanHosts`.
Export the not-granted sentinel as `ErrFreeboxNotGranted` from the freebox package so
callers can map it to a 409.

Then:

- `channels.Service.ListFreeboxLanHosts` delegates to it (keeps `ErrFreeboxNotGranted`
  mapping in the handler layer).
- The job calls it directly with `jctx.DBService` + `jctx.Services.Credentials`.

## API

Extend the existing discovery handler/service (`server/internal/handlers/discovery/`),
keeping all routes under `/api/v1/orgs/:org/discovery`:

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/discovery/freebox-scans` | admin | Body `{ "channelUid": "..." }`. Validates the channel is a paired Freebox channel (fail fast — `409 FREEBOX_NOT_GRANTED`), checks no discovery job is already running for the org, then creates a `freebox_lan_discovery` job. Returns `{ "data": <job> }` — same shape as `POST /scans`. |

Other changes in the same handler/service:

- `ListScans` (`GET /discovery/scans`) lists **both** `network_discovery` and
  `freebox_lan_discovery` jobs (newest first). Update the `ListJobsOptions.Type`
  filter to accept a slice or query both.
- `ListHosts` (`GET /discovery/hosts`) gains a `?source=lan,freebox` filter
  (singular param, comma-separated per the API convention), added to
  `ListHostsOptions.Sources []DiscoverySource` and the `WHERE source IN (?)` clause.
- `checkAlreadyRunning` is generalized to check the job type passed to it so it can
  guard both `network_discovery` and `freebox_lan_discovery` (per-org, per-type).

`DiscoveredHost` JSON gains `"source"` automatically since the model is serialized
directly. No DTO changes.

### Error codes

Add to `server/internal/handlers/discovery/handler.go`:

```go
ErrorCodeFreeboxNotGranted base.ErrorCode = "FREEBOX_NOT_GRANTED"
```

## Frontend (`web/dash0`)

> Check `http://localhost:4000/dash0/orgs/default/design-reference` before building —
> reuse `Badge`, `Select`/segmented control, table primitives from there.

### `web/dash0/src/api/hooks.ts`

- Add `source: "lan" | "freebox"` to `DiscoveredHost` (line ~3013).
- Add an optional `source?: string` arg to `useListCandidateHosts` → appended as
  `?source=` query param.
- Add `useStartFreeboxScan(org)` mutation (POST `/api/v1/orgs/${org}/discovery/freebox-scans`),
  invalidates `["discoveryScans", org]` on success.

### `web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx`

- Add a **Source** column header to the table (`<TableHead>{t("source")}</TableHead>`).
- Add a `<TableCell>` in `HostRow` that renders a `<Badge>` with
  `t(host.source === "lan" ? "sourceLan" : "sourceFreebox")`.

### `web/dash0/src/routes/orgs/$org/discovery.index.tsx`

- Show a source badge per row in the scans list (derive from job `type`:
  `network_discovery` → "LAN", `freebox_lan_discovery` → "Freebox").
- Add a source filter (All / LAN / Freebox) — a segmented control or `<Select>`
  above the scans table — that narrows the displayed list client-side.
- Add a **"Discover via Freebox"** action alongside the existing "New scan" button:
  a dropdown (or a second button) that lists paired Freebox channels (reuse the
  existing Freebox channel listing hook) and calls `useStartFreeboxScan` on
  selection. Show a toast on success/failure.

### i18n

Add keys to `web/dash0/src/locales/{en,fr,de,es}/discovery.json`:

| Key | English value |
|---|---|
| `source` | "Source" |
| `sourceLan` | "LAN" |
| `sourceFreebox` | "Freebox" |
| `filterBySource` | "Filter by source" |
| `allSources` | "All sources" |
| `discoverViaFreebox` | "Discover via Freebox" |
| `selectFreeboxChannel` | "Select Freebox channel" |
| `noFreeboxChannels` | "No paired Freebox channels" |
| `freeboxScanStarted` | "Freebox discovery started" |
| `freeboxScanFailed` | "Failed to start Freebox discovery" |

## Files to create / modify

### New files

- `server/internal/db/sqlite/migrations/030_discovered_hosts_source.{up,down}.sql`
- `server/internal/db/postgres/migrations/030_discovered_hosts_source.{up,down}.sql`
- `server/internal/integrations/freebox/lanlookup.go` (+ `lanlookup_test.go`)
- `server/internal/jobs/jobtypes/job_freebox_lan_discovery.go` (+ `_test.go`)

### Modified files

**Backend**
- `server/internal/db/models/discovered_host.go` — `DiscoverySource` type + constants,
  `Source` field, `NewDiscoveredHost` signature.
- `server/internal/jobs/jobdef/types.go` — `JobTypeFreeboxLanDiscovery`.
- `server/internal/jobs/jobtypes/registry.go` — register `FreeboxLanDiscoveryJobDefinition`.
- `server/internal/jobs/jobtypes/job_network_discovery.go` — pass `DiscoverySourceLAN`,
  update upsert conflict target.
- `server/internal/handlers/discovery/service.go` — update `UpsertDiscoveredHost`
  conflict target, add `StartFreeboxScan`, generalize `checkAlreadyRunning`,
  add `Sources` to `ListHostsOptions`.
- `server/internal/handlers/discovery/handler.go` — add `POST /freebox-scans` route +
  handler, update `ListScans` to both types, add `source` param to `ListHosts`.
- `server/internal/handlers/channels/freebox_lan.go` — delegate to
  `freebox.ListLanHostsForChannel`.

**Frontend**
- `web/dash0/src/api/hooks.ts`
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.index.tsx`
- `web/dash0/src/locales/{en,fr,de,es}/discovery.json`

**Docs**
- `wiki/api-specification.md` — `POST /discovery/freebox-scans`, `source` field,
  `?source=` filter on `GET /discovery/hosts`.

## Tests

- **Model**: `NewDiscoveredHost(..., DiscoverySourceLAN)` sets `Source = "lan"`.
- **Migration** (`server/internal/db/sqlite/migrations_test.go`): up/down round-trip;
  existing rows default to `source = 'lan'`.
- **Upsert** (discovery service test): same `(org, ip)` from different sources → two
  rows; same `(org, ip, source)` → updates in place (no duplicate).
- **Shared resolver** (`lanlookup_test.go`): ungranted channel returns
  `freebox.ErrFreeboxNotGranted`; granted channel returns hosts; router host filtered.
- **Job** (`job_freebox_lan_discovery_test.go`): table-driven with a stub freebox
  client; asserts hosts persisted with `source='freebox'`; per-host errors are
  non-fatal.
- **Service/handler**: `StartFreeboxScan` rejects ungranted channel (409); guards
  duplicate in-flight (409); `ListHosts` honors `?source=freebox`; `ListScans`
  returns both job types.
- **Playwright** (`web/dash0/e2e/discovery.spec.ts`): Source column visible in host
  table; source filter in scans list; "Discover via Freebox" button visible for admin.
- Standard rules throughout: `t.Parallel()`, `testify/require`, `r := require.New(t)`.

## Verification

```bash
make migrate            # applies 030 on sqlite + postgres
make lint test          # backend
make build              # full build (frontend type-check included)
make test-dash          # Playwright
```

Manual (run `make dev-test`, credentials from CLAUDE.md):

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# existing LAN-scan hosts now carry source='lan'
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/discovery/hosts' | jq '.data[].source'

# launch a Freebox discovery run (requires a paired Freebox channel)
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"channelUid":"<freebox-channel-uid>"}' \
  'http://localhost:4000/api/v1/orgs/default/discovery/freebox-scans' | jq .

# filter by source
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/discovery/hosts?source=freebox' | jq '.data'
```

UI (`/dash0/orgs/default/discovery`): confirm Source badges on both the scans list
and the per-job host table; confirm source filter narrows the scans list; confirm
"Discover via Freebox" creates a run whose hosts can be promoted and dismissed.

## Implementation plan

1. Migration `030` (both backends) + `migrations_test` up/down round-trip. `make lint test`.
2. Model: `DiscoverySource` + constants, `Source` field, `NewDiscoveredHost` signature;
   update `job_network_discovery.go` caller + conflict target. `make lint test`.
3. Shared `freebox.ListLanHostsForChannel`; refactor `channels.ListFreeboxLanHosts` to
   delegate; tests. `make lint test`.
4. `freebox_lan_discovery` job type, registry entry, job impl, test. `make lint test`.
5. Discovery service/handler: `StartFreeboxScan`, `freebox-scans` route, `ListScans`
   both types, `source` filter, tests. `make lint test`.
6. Frontend: hooks, Source column, scans-list source filter + badges, Freebox launcher,
   i18n; update `wiki/api-specification.md`. `make build lint test-dash`.
7. QA loop: `make build lint test test-dash`; completeness audit + archive + merge.

## Implementation Plan

1. **Migration 030 (both backends).** Add `030_discovered_hosts_source.{up,down}.sql`
   for sqlite and postgres. `up`: `ADD COLUMN source TEXT NOT NULL DEFAULT 'lan'`
   (backfills existing rows), drop `idx_discovered_hosts_org_ip_active`, create
   `idx_discovered_hosts_org_ip_source_active` on `(organization_uid, ip, source)`
   with the same partial predicate. `down`: reverse. Add a sqlite migration test
   asserting the column exists and existing rows default to `lan`.

2. **Model.** In `discovered_host.go` add `DiscoverySource` type + `DiscoverySourceLAN`
   / `DiscoverySourceFreebox` constants, the `Source` field, and change
   `NewDiscoveredHost(orgUID, jobUID, ip string, source DiscoverySource)`. Update
   `job_network_discovery.go` to pass `DiscoverySourceLAN` and change its upsert
   conflict target to include `source`. Model unit test for the new field.

3. **Shared Freebox LAN resolver.** Export `ErrFreeboxNotGranted` from the freebox
   package. New `freebox/lanlookup.go`: `ListLanHostsForChannel(ctx, dbSvc, creds,
   orgUID, channelUID)` resolving channel → validate org+type → decrypt app token →
   build client → `ListLanHosts`. Refactor `channels.ListFreeboxLanHosts` to delegate
   (keep the handler-layer `ErrFreeboxNotGranted` 409 mapping). `lanlookup_test.go`:
   ungranted → `ErrFreeboxNotGranted`; granted → hosts.

4. **Freebox job.** `JobTypeFreeboxLanDiscovery` in `jobdef/types.go`, registry entry,
   `job_freebox_lan_discovery.go` (`{channelUid}` config, calls resolver, maps LanHost
   → DiscoveredHost source='freebox', inline per-host upsert, non-fatal errors).
   `_test.go` with a stub freebox server.

5. **Discovery service/handler.** Update `UpsertDiscoveredHost` conflict target; add
   `StartFreeboxScan` (validates channel via resolver, guards in-flight per-type);
   generalize `checkAlreadyRunning(ctx, orgUID, jobType)`; add `Sources []DiscoverySource`
   to `ListHostsOptions` + `WHERE source IN`; `ListScans` lists both job types;
   `POST /freebox-scans` route + handler + `ErrorCodeFreeboxNotGranted`; `?source=`
   param on `ListHosts`. Service + handler tests.

6. **Frontend.** `hooks.ts`: `source` on `DiscoveredHost`, `source?` arg on
   `useListCandidateHosts`, `useStartFreeboxScan`. Source column + badge on
   `discovery.$jobUid.tsx`. Scans-list source badges + filter + "Discover via Freebox"
   launcher on `discovery.index.tsx`. i18n keys in 4 locales. Playwright assertions.
   Update `wiki/api-specification.md`.

7. **QA + audit + archive + merge.**
