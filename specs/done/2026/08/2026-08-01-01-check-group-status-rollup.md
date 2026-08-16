---
model: sonnet
effort: high
---

# Check groups have no aggregated status, so a host monitored by several checks can't be read as one unit

## Problem

A common real-world pattern (see the acmetech config export) is 2–4 checks of
different types probing the *same* host — TCP :443, HTTP, TLS-certificate expiry,
RDP. When that host dies they all fail together, and the natural mental model is
"one thing is down", not "four things are down".

The alerting side already treats a group as one unit: group incidents open a
single incident titled "N/M checks down" with per-member state
([incidents/service.go:918](server/internal/handlers/incidents/service.go:918),
`incident_member_checks`). But the *status* side has nothing: `CheckGroup`
carries only identity fields plus a `CheckCount` scanonly
([check_group.go:10-25](server/internal/db/models/check_group.go:10)), and the
check-groups API ([handlers/checkgroups/](server/internal/handlers/checkgroups/))
returns no status information at all. Every consumer that wants "is this group
healthy?" has to fetch all member checks and aggregate client-side — which is why
the dashboard list can't collapse a group into one status row and status pages
can't display a group (follow-up specs `2026-08-01-02` and `2026-08-01-03`).

Industry note: this is the settled pattern among peers — Better Stack exposes an
`aggregate_state` (up/warn/down/maintenance) per group; Uptime Kuma's group
monitor uses bare worst-of and its top complaint is the missing "degraded" middle
tier (louislam/uptime-kuma#5953). We already have the richer vocabulary:
`CheckStatusDegraded` (7) and `CheckStatusWarning` (8) exist precisely for
summary/display purposes ([check.go:26-34](server/internal/db/models/check.go:26)).

## Proposal

Add a **derived, read-time** group status — no new stored state, no new state
machine, no change to incidents or notifications.

1. **Rollup semantics** (the important part — implement exactly this, in one
   shared helper, e.g. `models.RollupGroupStatus(counts map[CheckStatus]int) CheckStatus`):
   - consider only **enabled** member checks;
   - all considered members `down` → `down`;
   - some but not all `down` → `degraded`;
   - no `down`, ≥1 `warning` → `warning`;
   - no `down`/`warning`, ≥1 `validating` → `validating`;
   - otherwise ≥1 `up` → `up`;
   - no considered members, or only `created` → `created`.

   This is deliberately Better-Stack-shaped (partial failure reads as degraded,
   a TLS warning doesn't paint the host red) and reuses only existing wire
   statuses (`String()` at [check.go:40](server/internal/db/models/check.go:40)),
   so the dashboard status colors/labels work unchanged.

2. **Storage query**: one `GROUP BY check_group_uid, status` count over `checks`
   (status column: [check.go:147](server/internal/db/models/check.go:147)),
   scoped to the org and non-deleted checks — same shape as the existing
   `CheckCount` computation. Must work on both PostgreSQL and SQLite (no
   dialect-specific SQL).

3. **API surface**: on the check-group list and get responses
   ([handlers/checkgroups/handler.go](server/internal/handlers/checkgroups/handler.go)):
   - `status` (string) — the rollup;
   - `memberStatusCounts` (object, wire-status → count, enabled members only,
     omitting zero counts) so clients can render "3/4 up" without a second call.

   camelCase per REST conventions; update
   [openapi.yaml](server/internal/app/openapi/openapi.yaml) and regenerate
   (`make generate`). If the MCP `list_check_groups` tool mirrors the response
   shape, include the new fields there too.

4. **Tests**: table-driven Go tests for the rollup helper covering every branch
   above (including disabled-members-excluded and empty group), plus a
   service/handler test asserting the fields appear on list and get and that
   the counts exclude disabled and soft-deleted checks.

5. **Docs**: document the group status semantics wherever check groups are
   described in `web/docs/` (one short table of the rollup rules).

### Out of scope

- No stored status, no status-change events, no notifications on group status
  transitions — group *alerting* is already covered by group incidents.
- No change to `formatGroupTitle` / incident behavior.
- No dashboard or status-page consumption — that's specs `2026-08-01-02` and
  `2026-08-01-03`, which depend on this one.
- No per-region rollup.

## Implementation Plan

1. **Rollup helper** — new `server/internal/db/models/check_group_status.go`:
   `RollupGroupStatus(counts map[CheckStatus]int) CheckStatus` implementing the
   six branches exactly as specified (down/degraded/warning/validating/up/created).
   Table-driven tests in `check_group_status_test.go` covering every branch,
   including empty map and "only created" counts.

2. **Storage query** — add `GetCheckGroupStatusCounts(ctx, orgUID string)
   (map[string]map[models.CheckStatus]int, error)` to the `db.Service`
   interface (`internal/db/service.go`), implemented identically in
   `internal/db/postgres/postgres.go` and `internal/db/sqlite/sqlite.go`: one
   `TableExpr("checks")` select with `ColumnExpr("check_group_uid, status,
   COUNT(*) AS count")`, `Where(organization_uid, deleted_at IS NULL, enabled =
   true, check_group_uid IS NOT NULL)`, `GroupExpr("check_group_uid, status")`
   — no dialect-specific SQL (mirrors the existing `ListDistinctLabelKeys`
   pattern, minus the ILIKE).

3. **API surface** — `checkgroups.CheckGroupResponse` gains `Status string`
   and `MemberStatusCounts map[string]int` (omitempty). `convertGroupToResponse`
   takes the per-group counts map, rolls it up via `models.RollupGroupStatus`,
   and builds the wire-status-keyed count map (omitting zero entries).
   `ListCheckGroups` and `GetCheckGroup` (service.go) fetch
   `GetCheckGroupStatusCounts` once per call and pass the relevant group's
   counts in. `UpdateCheckGroup`'s post-update refetch does the same.
   `CreateCheckGroup` passes a nil counts map (brand-new group ⇒ `created`,
   no query needed).

4. **OpenAPI** — add `status` (enum: created/up/down/validating/degraded/
   warning) and `memberStatusCounts` (object, additionalProperties: integer)
   to the `CheckGroup` schema in `server/internal/app/openapi/openapi.yaml`;
   add `status` to the schema's `required` list. Regenerate the Go client via
   `go generate ./...` in `server/pkg/client/`.

5. **MCP tool** — `list_check_groups` (`internal/mcp/tools_groups.go`) already
   marshals `Service.ListCheckGroups`'s response verbatim, so the new fields
   appear automatically; no code change expected, just confirm in review.

6. **Tests** — rollup unit tests (step 1); a service-level test
   (`internal/handlers/checkgroups/status_test.go`, sqlite in-memory, mirrors
   `escalation_test.go`'s setup) asserting `status`/`memberStatusCounts` on
   both `ListCheckGroups` and `GetCheckGroup`, and that disabled and
   soft-deleted checks are excluded from the counts.

7. **Docs** — add a "Group status" section with a rollup-rules table to
   `web/docs/docs/features/check-groups.md`.

8. Run `make fmt`, then the scoped gate `make build-backend lint-back test`.
