---
model: sonnet
effort: medium
---

# Discovered checks have no region: same IP in two networks collides, and promotion ignores where it was found

First of four specs for agent-run network discovery (01 region on discovered checks, 02 scanner
ICMP fix, 03 agent-executed chunks, 04 dashboard and docs). Implement in that order.

## Problem
`discovered_checks` has no region. Its identity is the partial unique index
`(organization_uid, source, group_key, slug) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL`
(`server/internal/db/postgres/migrations/002_v0_2_0.up.sql:71-73`, sqlite `:91-93`), used as the
`ON CONFLICT` target in `server/internal/discovery/persist.go:56-76`.

Once spec 03 lets a private region's agent scan its own network, `10.0.0.5` found from `@dc1`
and `10.0.0.5` found from `@dc2` (or from the server) are different machines, but they upsert
into the same row: the last write wins and overwrites `job_uid`, name, config and group label.

Promotion ignores location too. `promoteOne` (`server/internal/handlers/discovery/service.go:607-644`)
calls `CreateCheck` with no `Regions`, so placement falls back to org defaults or 2 cloud regions
(`server/internal/handlers/checks/placement.go:319-330, 381-393`). A check promoted from a `@dc1`
scan would run from the cloud and fail. Decision (2026-10-21): a promoted discovered check always
inherits the region of the scan that found it.

## Proposal
1. Migration, next free `NNN` (030 on 2026-10-21; check `git tag --contains` per
   `wiki/conventions/migrations.md`), transactional `.tx.up.sql` / `.tx.down.sql`, in both
   `server/internal/db/postgres/migrations/` and `server/internal/db/sqlite/migrations/`:
   - `alter table discovered_checks add column region text not null default '';`
     (`''` = found by the server itself; Postgres gets a `comment on column`, SQLite an inline `--`).
   - Drop and recreate `idx_discovered_checks_identity_active` on
     `(organization_uid, source, group_key, slug, region)` with the same partial predicate. Keep the
     name: `server/internal/db/sqlite/migrations_test.go:376` asserts it.
   - Not nullable on purpose: NULLs are distinct in unique indexes on both engines, so server-side
     re-scans would duplicate rows (the bug migration `006_v0_5_0` fixed for results).
   - Down: restore the 4-column index, then drop the column.
2. Model `server/internal/db/models/discovered_check.go:34-51`: add
   `Region string bun:"region,notnull" json:"region,omitempty"`, and a region argument to
   `NewDiscoveredCheck`.
3. `UpsertDiscoveredChecks` (`server/internal/discovery/persist.go:22-29`) takes a `region`
   argument; add `region` to the conflict column list (`persist.go:57-58`). Existing callers pass
   `""`: `job_network_discovery.go:95`, `job_freebox_lan_discovery.go:117`,
   `job_container_discovery.go:116`, `job_kubernetes_discovery.go:142` (all in
   `server/internal/jobs/jobtypes/`).
4. Add `(*regions.Service).OwnsPrivateRegion(ctx, orgUID, region string) (bool, error)` in
   `server/internal/regions/`, built on `ParsePrivateRegion` (`regions.go:112-123`, canonical
   `@slug` only, legacy `@org/slug` refused) and `GetOrgCustomRegions` (`regions.go:351-372`).
   Spec 03 reuses it. Do not refactor `checks.privateLocationDefinition` here.
5. Promote (`server/internal/handlers/discovery/service.go:607-644`): when `row.Region != ""`,
   check `OwnsPrivateRegion`. If the org no longer owns it, refuse with
   `422 DISCOVERY_REGION_UNAVAILABLE` and create nothing (`NormalizeRegionsForOrg` does not check
   existence, `regions/normalize.go:58-96`). Otherwise set `Regions: []string{row.Region}`, which
   pins the check (`checks/placement.go:385-387`). Map the new error to 422 in
   `handlers/discovery/handler.go:319-320` (today everything there is a 500). Rows with `region ''`
   keep today's placement. No region override in `PromoteOverrides` (`service.go:55-58`).
6. `DismissGroup` without `jobUid` (`service.go:693-706`) today deletes every row of the org with
   that `group_key`. Scope it by region: accept an optional `region` in the request, default `''`,
   and match it. Existing callers keep today's effect, since every existing row has `region ''`.
7. Docs: `wiki/database-model/discovery.md` (new column, new identity key; also fix source
   `docker` → `container` at `:15, :31`), `wiki/api-specification/discovery.md` (row `region`
   field, dismiss-group `region`).

## Tests
- `server/internal/discovery/persist_test.go` (create if missing, SQLite and Postgres layers):
  same `(source, group_key, slug)` with region `''` and `'@dc1'` gives 2 rows; re-upserting the
  same region updates in place (1 row, no duplicate); a second server-side (`''`) scan does not
  duplicate (negative case for the NOT NULL choice).
- `server/internal/db/sqlite/migrations_test.go`: the index covers `region`; up then down then up
  round-trips.
- `server/internal/handlers/discovery/service_test.go`: promote a `@dc1` row of an owned region →
  check created and pinned to `["@dc1"]`; region deleted after the scan → 422
  `DISCOVERY_REGION_UNAVAILABLE` and no check; `''` row → unchanged placement.
- Same file: `DismissGroup` with `region '@dc1'` leaves the `''` row with the same IP untouched.
- `server/internal/regions/`: `OwnsPrivateRegion` true for an owned `@dc1`, false for another
  org's `@dc1`, false for a cloud slug, error/false for `@acmetech/dc1` (legacy form).
- `make test-postgres` for conflict-target parity.

## To verify
- How `PromoteChecks` reports a per-item failure in a bulk call (whole call 422, or per-item
  status): follow the existing shape, do not invent a new one.
