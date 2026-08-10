# Checks

Checks and everything addressed relative to a check: validation, config-as-code
export/import/apply, dependencies, cloning, labels, check types, groups,
severities, badges, and availability.

Notification channels attached to a check are documented in
[integrations.md](integrations.md).

## Checks

### GET /api/v1/orgs/:org/checks
List monitoring checks. Auth: required

Query parameters:
- `with` - comma-separated: `last_result`, `last_status_change`
- `labels` - filter by labels, format: `key1:value1,key2:value2`
- `checkGroupUid` - filter by check group UID
- `q` - free-text search
- `internal` - filter by internal flag
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100)

With `with=last_status_change`, `lastStatusChange` is served from the check row
itself (`checks.status` / `checks.status_changed_at`, maintained on the incident
path) — no query, no scan of `results`. It therefore reports the **derived**
check status change, the same value the `status` field carries, so an
unconfirmed blip does not reset the timer. It is **omitted** for a check that
has never recorded a transition (`status_changed_at IS NULL`): no fallback to
`createdAt`, and no fallback to re-deriving transitions from raw results — that
older behavior returned the raw-retention horizon (a timestamp that slid forward
on every compaction run) for any check that had been stable through the whole
retention window. See spec `2026-08-09-07`.

With `with=last_result`, each item's `lastResult` is the **slim** shape —
`{uid, status, timestamp, durationMs}`, no `output`/`metrics` — since no list
consumer (checks table, org dashboard, status dashboard) reads those fields
and they can be large (SSL cert chains, DNSBL details). The detail endpoint
(`GET /checks/:checkUid`) keeps the full `lastResult` including
`output`/`metrics`.

### GET /api/v1/orgs/:org/checks/stats
Aggregate check counters for the org, computed server-side with one SQL
`GROUP BY`. Auth: required

```json
{
  "total": 262,
  "enabled": 250,
  "disabled": 12,
  "byStatus": {
    "created": 2, "up": 240, "down": 6,
    "validating": 0, "degraded": 2, "warning": 0, "unknown": 0
  },
  "down": 6,
  "hardDown": 3
}
```

This exists because the list endpoint clamps `limit` to 100, so any counter
derived from a single page is wrong past 100 checks (GitHub issue #172).

Semantics:
- **Scope** — non-deleted, **non-internal** checks: exactly the set
  `GET /checks` returns by default (`internal=false`), so the counters always
  agree with the list the operator can open.
- `total`, `byStatus`, `down` and `hardDown` span **enabled and disabled**
  checks alike; `enabled` / `disabled` partition the same set.
- `byStatus` always carries every known status key (`created`, `up`, `down`,
  `validating`, `degraded`, `warning`, `unknown`) — zero when empty — so
  clients can index it unguarded. Keys are the same tokens the list response's
  `status` field carries.
- `down` = status in (`down`, `error`, `timeout`); `hardDown` = status in
  (`down`, `error`). `error`/`timeout` are *result*-level statuses that a
  check-level status never holds, so today both equal `byStatus.down`.
- **Cached ~1 minute per org, in memory.** The response can lag a check
  create/delete or a status flip by up to the TTL; there is no invalidation.
  Consumers needing an exact, immediately consistent count should read
  `pagination.total` from the list endpoint instead.

### POST /api/v1/orgs/:org/checks
Create a new check. Type can be inferred from the config URL. Name and slug are auto-generated if omitted. Auth: required

### GET /api/v1/orgs/:org/checks/:checkUid
Get a single check by UID or slug. Auth: required

Query parameters:
- `with` - comma-separated optional includes (e.g., `last_result`)

With `with=last_result`, `lastResult` is the **full** shape (`uid`, `status`,
`timestamp`, `durationMs`, `output`, `metrics`) — the detail page renders
Output, Metrics, and the SSL-chain card from it. Contrast with the list
endpoint (`GET /checks`), which returns a slim `lastResult` without
`output`/`metrics`.

### PUT /api/v1/orgs/:org/checks/:slug
Upsert a check by slug (create if not exists, update if exists). Auth: required

Request body optionally carries `dependsOn`, pointer-typed (`*[]…`) so the
handler can distinguish three states:
- **absent** (`null` / field missing) → existing dep edges untouched. This
  is the back-compat default for tooling that doesn't know about deps —
  partial PUT must not nuke deps.
- **explicit empty array** (`[]`) → all dep edges for this check are
  deleted.
- **non-empty array** → set the dep edges to exactly this list (destructive
  sync). All cycle / self-edge / cross-org / kind / duplicate validators
  run before any write; any failure aborts the whole operation. Caveat: the
  dep apply currently runs after the check upsert outside any wrapping
  transaction — a failed dep apply leaves the check itself updated. A
  follow-up will move the whole flow into a single transaction.

### PATCH /api/v1/orgs/:org/checks/:checkUid
Update a check. Auth: required

### DELETE /api/v1/orgs/:org/checks/:checkUid
Delete a check (soft delete). Auth: required

### POST /api/v1/orgs/:org/checks/:checkUid/clone
Duplicate an existing check — copies configuration, labels, and channel
attachments into a new check with a fresh uid/slug. Auth: required

### GET /api/v1/orgs/:org/checks/:checkUid/events
List events for a specific check. Auth: required

Query parameters:
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

### GET /api/v1/orgs/:org/checks/:check/results/:uid
Get one result of a check by uid, with the full payload
(`output`, `metrics`). Auth: required

### GET /api/v1/orgs/:org/checks/:check/availability
Availability (uptime ratio) for a check over a window. Auth: required

## Validation

### POST /api/v1/orgs/:org/checks/validate
Validate a check configuration without persisting. Auth: required

Request body accepts the same shape as `POST /checks` plus optional
`dependsOn` (slug-keyed) and `slug` (so cycle / self-edge / duplicate /
cross-org / unknown-parent validators can run before the check exists).
Returns `{"valid": true}` or `{"valid": false, "fields": [...]}` with one
field-level entry per failing validator.

## Config-as-code: export / import / apply

### GET /api/v1/orgs/:org/checks/export
Export all checks as JSON. Auth: **admin** (org admin role required)

Each `ExportCheck` carries an optional `dependsOn` array of
`{parentSlug, kind, description?}` entries, sorted by `parentSlug` for
deterministic diffs. The field is `omitempty` — exports for orgs with no
dep edges stay byte-identical to the pre-dependsOn shape.

> **Back-compat note (2026-06-20):** export/import were previously gated by
> authentication only (any org member). They are now **admin-only**, alongside
> the new apply endpoint, because they read/mutate the whole check set. Scripts
> that called these as a non-admin user must switch to an admin token.

### POST /api/v1/orgs/:org/checks/import
Import checks from an export document. Auth: **admin** (org admin role required)

The body is accepted as **JSON or YAML** (sniffed from `Content-Type` and the
first non-space byte, same as `/apply`): export emits JSON, but a hand-authored
or converted manifest is just as likely to be YAML, and both parse to the same
document. Malformed input is still a `422 VALIDATION_ERROR`.

Two-pass when any entry carries `dependsOn`: pass 1 upserts every check
unchanged, pass 2 resolves `parentSlug` → check UID against the now-current
org state and applies an additive merge of edges (new edges created;
existing edges with same kind+description are no-ops; differing edges are
updated). Cycle / self-edge / unknown-parent failures are reported per row
in the existing `errors` array. Pass 2 is skipped silently for any check
whose pass-1 upsert failed, with an explicit
`skipped dependsOn: pass-1 upsert failed for this check` error.

### POST /api/v1/orgs/:org/checks/apply
Reconcile checks against a declarative manifest (config-as-code). Auth:
**admin** (org admin role required). This is the *reconcile sibling* of
`/import` — idempotent upsert-by-slug plus delete-by-absence within a bounded,
opted-in managed scope.

**Request body.** The existing export document shape (`{version, organization,
checks[]}`), accepted as **JSON or YAML** (sniffed from `Content-Type` and the
first non-space byte). YAML is the hand-authoring surface; JSON is what export
emits — both parse to the same plan.

**Managed scope.** Apply stamps every check it owns with a reserved label
`solidping.io/managed=<manifest-name>`, where the manifest name is the document's
`organization` field (falling back to the org slug). The reconcile scope is
exactly the checks carrying that label. Hand-created checks (no managed label)
are reported as `unmanaged` and are **never** adopted, modified destructively,
or deleted.

**Plan / reconcile semantics.** Matching is on `slug` within the managed scope:
- `create` — slug in the manifest, absent from the org.
- `update` — managed slug present in both.
- `unmanaged` — slug exists **without** the managed label (reported only).
- `delete` — managed check absent from the manifest (delete-by-absence).
- `rename` — a manifest check with `previousSlug` (or `uid`) referencing an
  existing managed check reconciles the rename in place instead of delete+create.

**Secret references.** Config string values may contain `${env:NAME}` and
`${param:KEY}` references, resolved **server-side at apply time** (env vars; the
`parameters` table — org-scoped first, then system-wide) into the existing
encrypted `config_private` envelope. The committed manifest stays secret-free.
A missing/unresolvable reference is a hard apply error. When
`SP_ENCRYPTION_MASTER_KEY` is unset (plaintext fallback), resolving a secret ref
emits a `warnings[]` entry rather than refusing — the resolved value lands in
plaintext config.

**Deletion safety (belt-and-suspenders).** Delete-by-absence happens **only**
when all of: (a) `?prune=true` is set, (b) the check carries the managed label,
and (c) the delete count is within the deletion cap (default 10). Beyond the
cap, apply refuses with `409 CONFLICT` unless `?force=true`.

Query parameters:
- `dryRun=true` — compute and return the plan only; mutate nothing.
- `prune=true` — enable delete-by-absence for managed, absent checks.
- `force=true` — lift the deletion cap for this apply.
- `deletionCap=<n>` — override the default cap (0 ⇒ default 10).

**Response** (extended import result):
```json
{
  "manifest": "default",
  "dryRun": false,
  "pruned": true,
  "created": 1, "updated": 2, "deleted": 1, "unmanaged": 0,
  "plan": [{"slug": "api", "action": "update"}, {"slug": "old", "action": "delete"}],
  "warnings": [],
  "errors": []
}
```

### POST /api/v1/orgs/:org/checks/import/convert
Import checks from a third-party monitoring tool. Auth: **admin** (org admin
role required).

Converts a foreign configuration into the canonical export document and then
feeds it through the **existing** `ApplyChecks` path — there is no second
import pipeline, so slug upsert, group auto-creation, config validation and
per-check error collection all behave exactly as they do for `/apply`.

Query parameters:
- `source` — `gatus` | `betterstack` | `uptime-kuma` (required).
- `dryRun=true` — compute and return the plan only; mutate nothing.

Request body, per source:

| Source | Body | Notes |
|---|---|---|
| `gatus` | the raw `config.yaml` | Gatus has no config-export API. |
| `uptime-kuma` | the raw backup JSON | Settings → Backup → Export (Kuma 1.x). |
| `betterstack` | `{"token": "...", "baseUrl": "..."}` | The server fetches every page of `/api/v2/monitors` **and** `/api/v2/heartbeats`. `baseUrl` is optional (tests / proxies). The token is used transiently for that fetch and is **never persisted, logged, or echoed in an error**. |

Each converted document is applied under a per-source managed manifest
(`solidping.io/managed=gatus` / `betterstack` / `uptime-kuma`), so re-importing
the same source updates in place and stays idempotent. `prune` is never enabled
for a conversion — a foreign export is a partial view of the org.

**Response** (the apply/dry-run shape, plus conversion metadata):
```json
{
  "source": "gatus",
  "converted": 12,
  "manifest": "gatus",
  "dryRun": true,
  "created": 12, "updated": 0, "unmanaged": 0,
  "plan": [{"slug": "back-end", "action": "create"}],
  "errors": [],
  "warnings": [
    {"item": "back-end", "field": "conditions",
     "message": "condition \"[RESPONSE_TIME] < 300\" has no SolidPing equivalent and was dropped"}
  ]
}
```

`warnings` lists everything that did not map faithfully — unmappable
conditions/monitor types, credentials that were deliberately not imported,
notification bindings, and the heartbeat/push URL change. Unmappable items
never block the import: what maps is imported, the rest is reported.

## Check Dependencies

Parent/child DAG edges between checks, used to suppress downstream noise. All
routes require auth.

### GET /api/v1/orgs/:org/checks/:check/dependencies
List the dependency edges of a check.

### POST /api/v1/orgs/:org/checks/:check/dependencies
Create a dependency edge. Cycle, self-edge, cross-org, kind, and duplicate
validators run before the write.

### PATCH /api/v1/orgs/:org/checks/:check/dependencies/:uid
Update an edge (kind, description).

### DELETE /api/v1/orgs/:org/checks/:check/dependencies/:uid
Delete an edge.

### GET /api/v1/orgs/:org/dependencies
Whole-org dependency graph (nodes + edges), for the graph view.

## Labels

### GET /api/v1/orgs/:org/labels
Autocomplete suggestions for label keys (or values for a given key) used by checks in the org. Returns rows sorted by usage count DESC, then `value` ASC for stable ties. Auth: required.

Query parameters:
- `key` - if omitted, lists distinct keys; if provided, lists distinct values for that key
- `q` - case-insensitive prefix filter on the returned `value`
- `limit` - page size (default 50, silently clamped to max 200)

Response:
```json
{
  "data": [
    {"value": "environment", "count": 12},
    {"value": "team", "count": 8}
  ]
}
```

`count` is the number of distinct checks carrying that key (or key/value pair). Empty result returns `{"data": []}` (200), not 404.

## Check Types

### GET /api/v1/check-types
List all check types with metadata and server-level activation status. Auth: public

### GET /api/v1/check-types/samples
List sample configurations for all check types. Supports `?type=` filter. Auth: public

### GET /api/v1/orgs/:org/check-types
List check types resolved for the organization (merges server and org settings). Auth: required

## Check Groups

### GET /api/v1/orgs/:org/check-groups
List check groups. Auth: required

### POST /api/v1/orgs/:org/check-groups
Create a check group. Auth: required

### GET /api/v1/orgs/:org/check-groups/:uid
Get a check group. Auth: required

### PATCH /api/v1/orgs/:org/check-groups/:uid
Update a check group. Auth: required

### DELETE /api/v1/orgs/:org/check-groups/:uid
Delete a check group. Auth: required

## Severities

Named channel sets referenced by escalation-policy steps (see
[on-call.md](on-call.md)). All routes require auth.

### GET /api/v1/orgs/:org/severities
List severities.

### POST /api/v1/orgs/:org/severities
Create a severity.

### GET /api/v1/orgs/:org/severities/:uid
Get a severity.

### PATCH /api/v1/orgs/:org/severities/:uid
Update a severity.

### DELETE /api/v1/orgs/:org/severities/:uid
Delete a severity.

## Badges

### GET /api/v1/orgs/:org/checks/:check/badges/:components
Get a status badge for a check. The `:components` path segment selects which
parts the badge renders (the route parameter is `components`, not `format`).
Auth: public
