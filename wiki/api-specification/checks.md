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
  "hardDown": 3,
  "availability24h": 99.97
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
- `availability24h` — `100 * success / total` over the trailing 24h window of
  **results**, not checks: combines `hour` rollup rows
  (`successful_checks`/`total_checks`) and `raw` rows (success = up +
  warning; excludes lifecycle markers and reaped/abandoned attempts, mirroring
  `RawAvailability`). `null` when the window has no countable data (an empty
  or brand-new org) — never a fabricated 100 (spec 2026-08-26-09).
- **Cached ~1 minute per org, in memory.** The response can lag a check
  create/delete or a status flip by up to the TTL; there is no invalidation.
  Consumers needing an exact, immediately consistent count should read
  `pagination.total` from the list endpoint instead.

### POST /api/v1/orgs/:org/checks
Create a new check. Type can be inferred from the config URL. Name and slug are auto-generated if omitted. Auth: required

**`internal` is not writable.** It marks server-created plumbing (the worker
self-stat checks) and is what exempts a check from `maxChecks`, from the
checks-per-minute demand figure and from the per-org execution rate limit — so
a request that carries it is refused with a `422 VALIDATION_ERROR` naming the
field, on POST, PATCH, PUT-by-slug and on import/apply alike (spec
`2026-08-27-01`). It stays readable on every check response, and
`GET /checks?internal=` still filters on it. A **clone** of an internal check is
a normal, metered check: the flag is not copied.

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

### GET /api/v1/orgs/:org/checks/:check/availability/buckets
Bucketed availability over an **arbitrary** window — the data behind the check
detail chart's availability strip. Auth: required

| Query | Required | Meaning |
|---|---|---|
| `from` / `to` | yes | RFC3339 window bounds, `to > from` |
| `bucket` | no | Cell width, a Go duration that must be a whole positive multiple of **1h** (max 200 cells). Omitted → the smallest hour-multiple keeping the count ≤ 60 |
| `region` | no | Scope to one probe region; omitted sums up/total across regions (never averages their percentages) |

Deliberately a separate route from `/availability`: that one speaks trailing
calendar tokens plus a `tz`, has no `from`/`to`, and caps at 12 periods.

Response: `{ data: [cell…], window, bucketSeconds, windowStart, windowEnd,
region? }`. Each cell is
`{ periodStart, periodEnd, hasData, availabilityPct|null, totalChecks,
successfulChecks, status }` with `status` in `up|degraded|down|noData` — the
same classifier (and small-bucket guard) the public status page uses. The badge
SVG's uptime bar deliberately does **not** share it — it keeps a four-tier scale
with an extra orange band at ≥ 98% and no small-bucket guard, because badges are
check-scoped and stay on the global default thresholds (spec 2026-08-03-01). It
shares the bucketing engine, not the colour mapping.

Two rules worth knowing before consuming it:

- **Cells are aligned outward** to bucket boundaries, so the series can start
  before `from` and end after `to` (the engine keys every row on
  `periodStart.Truncate(bucket)`). `window` is the **exact** `[from, to)` fold —
  use it, not the sum of the cells, for a single headline figure.
- **`hasData: false` is a third state**, not zero and not 100. It is what a
  window reaching past day-tier retention returns, because a month rollup spans
  many cells and is never attributed to one.

Maintenance probes count exactly like any other probe here, matching the
availability table, status pages and badges — maintenance exclusion is
SLO-only.

## Validation

### POST /api/v1/orgs/:org/checks/validate
Validate a check configuration **or a whole config-as-code document** without
persisting. Auth: see the two floors below.

The route is **content-negotiated** (spec 2026-09-11-04): a body carrying a
top-level `checks` list — JSON *or* YAML — is a document and takes the document
path described in [the next section](#validating-a-whole-document); anything
else is a single check definition and behaves exactly as it always has.

| Body | Auth floor | Response shape |
|---|---|---|
| single check | `user` (the write floor — unchanged) | `{valid, fields[], warnings[]}` |
| whole document | **`viewer`** (reads nothing but the org's parameters) | `{valid, issues[], plan?}` |
| whole document + `?plan=true` | `admin` | …plus `plan` |

Request body accepts the same shape as `POST /checks` plus:

| Field | Purpose |
|---|---|
| `dependsOn` | slug-keyed edges, so the cycle / self-edge / duplicate / cross-org / unknown-parent validators run before the check exists |
| `slug` | validated for format **and** for uniqueness against the org's live checks |
| `regions` | the proposed region set (tunnel region rules, capability hints, and the per-minute projection — a check runs once per region per period) |
| `period` | the proposed interval (`HH:MM:SS` or a Go duration); unlocks the per-type bounds and the checks-per-minute projection |
| `enabled` | proposed enabled state, default true; a disabled check draws no rate budget |
| `excludeCheckUid` | the check being edited, so its own slug is not a collision and the projection REPLACES its stored row |

Returns `{"valid": …, "fields": [...], "warnings": [...]}`. Since spec
2026-08-26-05 the response reports **every** finding it can compute, not just
the first, and each entry carries a `severity`
(`error` | `warning` | `info`, **absent means `error`**) plus a stable machine
`code`. `valid` is false exactly when `fields` is non-empty — a warning never
blocks.

Blocking codes: `UNSUPPORTED_TYPE`, `INVALID_CONFIG`, `INVALID_PERIOD`,
`INVALID_SLUG`, `SLUG_TAKEN`, `INVALID_DEPENDS_ON`. `SLUG_TAKEN` is advisory by
nature — the value can be claimed between the answer and the save, which is why
creation still answers `409`.

Advisory codes: `REGION_NO_IPV6` / `REGION_NO_BROWSER` (a selected region's live
workers advertise no IPv6 egress / no headless Chrome — never a rejection,
because the advertised capability lags reality and the run-time probe is the
authority) and `ORG_RATE_OVER_LIMIT` on the `period` field (the proposed
period × regions would put the org's scheduled checks-per-minute demand over
its `maxChecksPerMinute` cap, so executions would be skipped; passive types are
exempt). `ORG_RATE_OVER_LIMIT` is deliberately never blocking — an over-limit
org has to be able to edit its way back under the cap — and it is the frontend's
cue to link to the check scheduling page.

**One validator, two callers.** The config-level rules (uniform timeout cap,
address family, tunnel reference, SMTP send-mode) live in one list that both
this dry run and the real create/update paths read
(`Service.configValidationErrors`): the write paths take the first error, this
endpoint turns every one into a finding. A rule added there is previewed and
enforced at once, or not at all.

### Validating a whole document

Same route, a document body. It is **member-level on purpose**: validating
writes nothing, and a CI job that only wants to know *"is this file valid?"*
must not need a write-capable token. Needing one is the structural reason third
parties kept their own re-implementations of the rules — and why those drifted
from the server the moment a check type was added (one org's validator reported
197 problems on that org's own export; 183 were a region spelling and 12 were
check types the validator predated).

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/yaml' --data-binary @config.yaml \
  'https://solidping.io/api/v1/orgs/acme/checks/validate'
```

```json
{
  "valid": false,
  "issues": [
    {"slug": "api", "field": "regions", "code": "REGION_FORMAT",
     "message": "region \"Paris!\" must be a slug or \"@private-location\""},
    {"slug": "db", "field": "type", "code": "UNKNOWN_TYPE",
     "message": "unsupported check type \"postgres9\""}
  ]
}
```

**Every** issue is reported, never the first only — a validator that costs a
round trip per defect is one nobody runs. `valid` is false exactly when `issues`
is non-empty.

`code` is the stable half of an issue; `message` is prose and may be reworded.
A CI job branches on the code and may allow-list classes it accepts. The closed
set (`checks.DocumentIssueCodes()` in the server, so this list cannot drift from
what is emitted):

| Code | Reported when |
|---|---|
| `UNSUPPORTED_VERSION` | `version` is not 1 or 2 |
| `MISSING_ORGANIZATION` | no `organization` |
| `INVALID_SECRETS_MARKER` | `secrets` is set to anything but `stripped` |
| `EMPTY_CHECKS` | `checks` is empty or absent |
| `MISSING_FIELD` | a check omits `name`, `slug`, `type` or `config` |
| `INVALID_SLUG` | a slug that is not kebab-case |
| `DUPLICATE_SLUG` | the same slug twice in one document |
| `INTERNAL_NOT_WRITABLE` | a check sets `internal` (server-owned) |
| `UNKNOWN_TYPE` | no checker implements the type |
| `INVALID_CONFIG` | the checker's own offline `Validate`, the timeout cap, the address-family rule |
| `INLINED_CREDENTIAL` | a config key the checker **declares secret** carries a literal value |
| `STATUS_FIELD_CONFLICT` | `expectedStatus` and `expectedStatusCodes` both set |
| `INVALID_PERIOD` | `period` is not a duration |
| `INVALID_LABEL` | a label key or value the database would refuse |
| `REGION_FORMAT` | a region that is neither a slug nor `@location` |
| `INVALID_DEPENDS_ON` | missing/self/duplicate/unknown parent, or a bad `kind` |
| `DEPENDENCY_CYCLE` | a cycle in the `dependsOn` graph |
| `UNRESOLVED_SECRET_REF` | a `${env:}`/`${param:}` reference that does not resolve **for this org** |

Everything but the last is decidable offline, which is exactly what
`sp checks validate config.yaml` runs with no token and no network.
`UNRESOLVED_SECRET_REF` needs the organization's parameters, so only this
endpoint reports it — and it is the same rule `/import` and `/apply` answer
`400` on.

Two things follow from the endpoint knowing the organization, and together they
are what makes "a document this endpoint calls valid is one `/import` and
`/apply` accept" true rather than aspirational:

- a `${param:…}` reference is resolved against the org's real parameters;
- the `secrets: stripped` suppression is applied only where the import merge it
  stands in for will actually happen — on a check that **already exists**. A
  stripped document whose checks are new is a create with nothing to merge, so
  the missing declared secret is reported here exactly as `/import` reports it.
  The offline validator cannot tell and assumes the check exists, which is the
  only assumption under which an export validates with no network.

With `?plan=true` (admin) the response also carries `plan`: the
[apply dry run](#post-apiv1orgsorgchecksapply), so one call answers both *"is it
valid?"* and *"what would it change?"*.

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

**`?dryRun=true` validates everything the real run validates** (spec
2026-09-10-01). Each entry is *planned* — the per-entry contract, the
created-vs-updated lookup, group resolution (an absent group is recorded as
would-create, never written), the upsert request built, and then the same
request validation the write path runs: label keys and values, the checker's
own `Validate` on the config, region resolution, period and alerting bounds,
and the `MaxChecks` quota counted across the whole document. A dry run
therefore returns the same `created` / `updated` / `errors` — the same per-item
error strings — the real run would, and writes nothing. Where it provably
cannot, it says so in `caveats[]` rather than over-claiming.

It used to return as soon as it had decided created-vs-updated, so a document
that could not possibly be written dry-ran to `{"created": N, "errors": []}`.

Response fields:

| Field | Meaning |
|---|---|
| `created` / `updated` | Entries created / updated (or, on a dry run, that would be) |
| `skipped` | Entries whose `dependsOn` edges were **not** applied — on a real run those whose own upsert failed, on a dry run every entry carrying `dependsOn` (pass 2 cannot resolve edges against state a dry run did not write) |
| `errors[]` | Per-entry `{index, slug, error, state?}` |
| `errors[].state` | Set **only** to `created-incomplete`: the check row was inserted, finishing it failed, and the compensating delete failed too — the row really is on disk. Absent means nothing was written for that entry |
| `dryRun` | Echo of the request |
| `caveats[]` | Dry run only: the validations **this** dry run could not perform without writing (`checks.DryRunCaveat`). `DryRunCaveatSlugRace` is unconditional — a concurrent create can claim a slug between the plan and the write. `DryRunCaveatSecretMerge` appears only when the document would update a check whose stored config is encrypted or region-sealed: the real update validates the *merge* of the document's config with those stored secrets, which a dry run cannot reproduce without decrypting a row it must not touch, so it validates the config as written instead |

**A failed entry leaves no check behind.** `CreateCheck` inserts the check row
before writing its labels; when anything after the insert fails it now performs
a compensating **hard** delete of the row it just inserted (a soft delete would
keep the slug claimed) and emits no `check.created` or activation event, so an
entry is counted as `created` only when it really was created. Before this, a
47-check import reported `created: 0, errors: [47 × label error]` while all 47
half-configured checks stayed on disk.

**Label keys** obey one rule everywhere — `^[a-z][a-z0-9-]{2,50}$`, values
non-empty and at most 200 characters — checked in Go before any write
(`models.ValidateLabels`) and mirrored by the `labels_key_check` CHECK on both
backends. A violating key is a per-entry `errors[]` message naming the key and
the rule, and a `400 VALIDATION_ERROR` on `POST /checks` / `PATCH /checks/:uid`
— never a driver string with a SQLSTATE in it. SQLite used to accept keys
Postgres could not store; migration `021` closed that.

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
`solidping-managed=<manifest-name>`, where the manifest name is the document's
`organization` field (falling back to the org slug). The key was
`solidping.io/managed` until spec 2026-09-10-01 — a spelling the Postgres
`labels_key_check` CHECK has always refused, so apply and the importers were
broken on Postgres and only appeared to work against SQLite. SQLite rows
carrying the old key are renamed by migration `021`. The reconcile scope is
exactly the checks carrying that label. Hand-created checks (no managed label)
are reported as `unmanaged` and are **never** adopted, modified destructively,
or deleted.

**Plan / reconcile semantics.** Matching is on `slug` within the managed scope:
- `create` — slug in the manifest, absent from the org.
- `update` — managed slug present in both, **and at least one field moves**.
  The entry carries `changes: [{field, from, to}]`.
- `unchanged` — managed slug present in both and the normalized effective state
  already matches. Writing it would change nothing.
- `unmanaged` — slug exists **without** the managed label (reported only).
- `delete` — managed check absent from the manifest (delete-by-absence).
- `rename` — a manifest check with `previousSlug` (or `uid`) referencing an
  existing managed check reconciles the rename in place instead of delete+create.

**`created=0 updated=0 deleted=0` with N `unchanged` is the machine-readable
"the file matches the instance".** Until spec 2026-09-11-04 `update` counted
every matched slug whether or not anything moved — an import of a file that was
byte-for-byte the current export answered `created=1 updated=482` — so the one
question config-as-code exists to answer had no answer short of a client-side
diff, and every external tool that grew one drifted. `sp checks diff` is now
presentation over this response rather than a second normalizer.

"Unchanged" is computed on the **normalized effective** config — the document's
config put through `normalizeCheckConfig`, regions resolved (so the folded
`@paris` and the long `@acme/paris` are the same region), periods compared as
durations, and both sides projected through the exporter's own code. The
projection is literally the function the exporter uses, so *a fresh export
plans as a no-op* is a property of the code rather than an agreement between
two implementations.

Two things a diff deliberately cannot claim, both reported as **masked**
(`"***"`) rather than silently called equal:
- a config key the exporter strips (a declared secret, or an export-redacted
  field) that the document nonetheless supplies — the stored value lives in an
  encrypted column a dry run must not open;
- any value containing a `${env:}`/`${param:}` reference, so a plan pasted into
  a ticket never publishes one. Only the reference itself is masked; the
  surrounding value is shown, because that is what names the field that moved.

And one field the plan reports but cannot apply: **`escalationThreshold`**. The
exporter emits it, but no request struct carries it — not `UpsertCheckRequest`,
not `POST /checks`, not `PATCH /checks/:uid` — so editing it in a tracked file
changes nothing anywhere. It is diffed all the same, and the response carries a
`warnings[]` entry naming the field and the affected slugs. Reporting a
difference nobody can apply is unpleasant; reporting `unchanged` for a file that
differs would be the same false no-op this endpoint exists to remove. Making the
field writable is its own spec.

`unmanaged` answers *who owns this check*, never *does it match*: an unmanaged
entry carries its `changes[]` too, so a first-time organization (where every
check is unmanaged) still gets a real answer rather than an empty one.

**Secret references.** Config string values may contain `${env:NAME}` and
`${param:KEY}` references. Since spec 2026-09-11-03 the **reference is what is
stored**: apply and import only *validate* that every reference resolves, and
the value is materialized at execution time — it never reaches the `config`
column and never comes back from `GET /checks/:uid` or `/checks/export`, so the
manifest round-trips as written. A missing or unresolvable reference is a hard
`400 VALIDATION_ERROR` from **both** endpoints, dry run included. A document
using `${env:}` gets one advisory `warnings[]` entry: it resolves on the process
that executes the check, which for a deported agent is that agent's environment.
See [`features/config-as-code.md`](../features/config-as-code.md#secret-references).

`${param:KEY}` reads the referencing organization's OWN parameters and nothing
else — no system-wide fallback, and org-managed rows live in a namespace the
platform never writes to, so no key an org admin can name resolves to platform
material. Manage them at
[`/orgs/:org/parameters`](orgs.md#organization-parameters) or with
`sp params set`.

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
  "created": 1, "updated": 1, "unchanged": 47, "deleted": 1, "unmanaged": 0,
  "plan": [
    {"slug": "api", "action": "update",
     "changes": [{"field": "config.url", "from": "\"https://acme.com/api\"",
                  "to": "\"https://acme.com/api-v2\""}]},
    {"slug": "web", "action": "unchanged"},
    {"slug": "old", "action": "delete", "reason": "managed check absent from manifest"}
  ],
  "warnings": [],
  "errors": []
}
```

`/import` answers with the same five counters and its own `plan[]`
(`{slug, action, changes?}`). `deleted` and `unmanaged` are structurally zero
there — import has no managed scope and never deletes by absence — and are
reported anyway so a CI job reads one shape from either endpoint.

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
(`solidping-managed=gatus` / `betterstack` / `uptime-kuma`), so re-importing
the same source updates in place and stays idempotent. `prune` is never enabled
for a conversion — a foreign export is a partial view of the org.

Re-converting an unchanged source answers `created=0 updated=0` with the whole
set in `unchanged` (spec 2026-09-11-04), which is how "nothing to do" is told
apart from "everything rewritten". The one exception is a source that inlines a
credential (a Better Stack basic-auth monitor, an UptimeRobot custom header):
the stored value lives in an encrypted column a dry run must not open, so that
entry is reported as an `update` whose `changes[]` are **masked** rather than
claimed equal.

**Response** (the apply/dry-run shape, plus conversion metadata):
```json
{
  "source": "gatus",
  "converted": 12,
  "manifest": "gatus",
  "dryRun": true,
  "created": 12, "updated": 0, "unchanged": 0, "unmanaged": 0,
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
