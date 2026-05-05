# Collapse `org_entitlements` columns into a single JSONB payload

## Context

The entitlements model shipped today (`specs/done/2026/05/2026-05-05-06-entitlements-model.md`)
mirrors a Go struct (`entitlements.Entitlements`) into ~17 typed columns on
`org_entitlements` (11 limits + 6 features + `allowed_check_types`). Adding a
new limit or feature currently means edits in **eight** places:

1. `server/internal/db/postgres/migrations/NNN_*.up.sql` — add column
2. `server/internal/db/sqlite/migrations/NNN_*.up.sql` — add column
3. `server/internal/db/models/org_entitlements.go` — add bun field
4. `server/internal/db/postgres/postgres.go` — add to `Set("col = EXCLUDED.col")` upsert chain
5. `server/internal/db/sqlite/sqlite.go` — same
6. `server/internal/entitlements/defaults.go` — add to `Limits`/`Features` struct
7. `server/internal/entitlements/service.go::merge()` — add per-field overlay
8. `server/internal/entitlements/service.go::toModel()` and `defaultFeatureFlag()` — add per-field copy + switch case

The shape on disk and the shape in code are already 1:1 isomorphic. The
column form buys little: there are no SQL `WHERE feature_X = true` queries in
the codebase today (verified with grep — only org-scoped reads on
`organization_uid`), the audit log already stores full JSONB snapshots in
`before_snapshot` / `after_snapshot`, `metadata` is already JSONB, and
`allowed_check_types` is *already* JSON-encoded text in SQLite for cross-DB
portability — the typed-column purity is half-fictional.

This spec collapses the limits/features/allowed-types surface into a single
`payload jsonb` column whose shape is owned by the
`entitlements.Entitlements` Go struct, and keeps as columns only the fields
we actually query, index, or constrain (`source`, `external_ref`,
`expires_at`, `last_synced_at`, plus identity/lifecycle).

## Goals

- Single source of truth for the entitlement schema: the Go struct.
- Adding a limit or feature touches **one** file (struct + tests).
- No loss of audit-log fidelity (it already serializes the full snapshot).
- No loss of indexability for fields actually used as lookup keys.
- Forward-compatibility: a `version` discriminator inside the payload so
  future shape changes can be handled at unmarshal time without a migration.
- Schema stays cross-DB portable (postgres `jsonb`, sqlite `text` holding
  JSON — same pattern already used for `metadata` and `allowed_check_types`).

## Non-goals

- No change to the public API contract (`GET/PUT/PATCH /api/v1/orgs/$org/entitlements`).
  Wire format on the way in/out is identical.
- No change to defaults, resolver semantics, staleness rules, or the audit
  log table.
- No new fields. Refactor only.
- Not addressing whether `metadata` and `payload` should be merged — keep
  them separate (different ownership: `payload` is structured-by-OSS,
  `metadata` is opaque pass-through from the billing service).

## Why this is worth doing now

- The model is six hours old and has zero stored rows in production. Cost of
  change ≈ writing a migration + rewriting `merge`/`toModel`. No backfill
  data to lose.
- Every future entitlement (and the SaaS roadmap implies several — per-tier
  rate limits, per-region quotas, file-storage caps once
  `2026-05-03-48-files-storage-foundation` lands) compounds the 8-place
  edit cost.
- The original spec's "Why columns" rationale (type-checking, migration
  safety, analytics queries) is principled but doesn't match the actual
  access pattern: bun unmarshalling into a typed struct preserves type
  safety, JSON-key renames are no harder than column renames (both need a
  data migration), and the analytics queries don't exist.

## Counter-arguments (and why we accept them)

- **"You lose `WHERE feature_sso = true`."** True. We never wrote one. If we
  ever need it, postgres supports `WHERE payload->'features'->>'sso' = 'true'`
  with an expression index. SQLite has `json_extract`. Acceptable cost.
- **"You lose CHECK constraints on individual limits."** No CHECK constraints
  exist today on these columns. Validation already lives in the service layer
  (`server/internal/entitlements`). No change.
- **"JSONB schema migration is harder than `ALTER TABLE`."** For renames it's
  about equal — both require a data-touching migration. For *additions* JSONB
  is strictly easier (no DDL, defaults handled by `omitempty` + struct
  default). For *removals* JSONB is also easier (drop the field; old payloads
  with the dead key are harmless because the struct just ignores unknown
  keys).
- **"Bun loses some compile-time guarantees."** The struct is the API; bun
  serializes/deserializes it via JSON. Type safety is preserved at the Go
  layer where it matters.

## Data model

No data backfill is needed: the entitlements model shipped today and there
are no stored rows to preserve. Either edit the existing migrations
(`017_org_entitlements.up.sql` postgres / `016_org_entitlements.up.sql`
sqlite) in place to land the new shape, or add a follow-up migration that
drops and recreates the table. **Recommended: edit in place** — there is
no production data, the original migrations are six hours old, and an
in-place edit keeps the migration history readable. If anyone ran
`./solidping migrate` already, they reset their dev DB (`SP_DB_RESET=true`
or delete `solidping.db`).

### Postgres `017_org_entitlements.up.sql` (rewritten in place)

```sql
CREATE TABLE org_entitlements (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

  payload           jsonb NOT NULL DEFAULT '{}'::jsonb,

  source            text NOT NULL CHECK (source IN
                       ('default','self-hosted','admin','billing-service')),
  external_ref      text,
  expires_at        timestamptz,
  last_synced_at    timestamptz,
  metadata          jsonb NOT NULL DEFAULT '{}'::jsonb,

  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX org_entitlements_external_ref_idx
  ON org_entitlements (external_ref) WHERE external_ref IS NOT NULL;

-- org_entitlement_audits is unchanged — keep its CREATE TABLE intact.
```

### SQLite `016_org_entitlements.up.sql` (rewritten in place)

```sql
CREATE TABLE org_entitlements (
  uid               text PRIMARY KEY,
  organization_uid  text NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

  payload           text NOT NULL DEFAULT '{}',

  source            text NOT NULL CHECK (source IN
                       ('default','self-hosted','admin','billing-service')),
  external_ref      text,
  expires_at        text,
  last_synced_at    text,
  metadata          text NOT NULL DEFAULT '{}',

  created_at        text NOT NULL DEFAULT (datetime('now')),
  updated_at        text NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX org_entitlements_external_ref_idx
  ON org_entitlements (external_ref);

-- org_entitlement_audits is unchanged.
```

The corresponding `.down.sql` files become a simple `DROP TABLE
org_entitlements; DROP TABLE org_entitlement_audits;` (matching the
pre-existing pattern).

### Final shape

```sql
CREATE TABLE org_entitlements (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

  -- Serialized entitlements.Entitlements struct minus break-out columns.
  -- Keys are owned by the struct's json tags; absent keys = "use default".
  payload           jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- Break-out columns: queried, indexed, or constrained.
  source            text NOT NULL CHECK (source IN
                       ('default','self-hosted','admin','billing-service')),
  external_ref      text,                     -- billing-service customer id, indexed
  expires_at        timestamptz,              -- staleness / trial end
  last_synced_at    timestamptz,              -- staleness signal for billing source
  metadata          jsonb NOT NULL DEFAULT '{}'::jsonb,  -- opaque pass-through

  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX org_entitlements_external_ref_idx
  ON org_entitlements (external_ref) WHERE external_ref IS NOT NULL;
```

`org_entitlement_audits` is unchanged — it already stores JSONB snapshots
of the full row, so the audit shape simply tracks the new payload format
going forward. Old audits remain valid (just describe the old column
shape); no rewrite needed.

## Code changes

### `server/internal/db/models/org_entitlements.go`

```go
type OrgEntitlements struct {
    UID             string `bun:"uid,pk,type:varchar(36)"`
    OrganizationUID string `bun:"organization_uid,notnull,unique"`

    Payload  EntitlementsPayload `bun:"payload,type:jsonb,notnull"`
    Metadata JSONMap             `bun:"metadata,type:jsonb,nullzero"`

    Source       EntitlementSource `bun:"source,notnull"`
    ExternalRef  *string           `bun:"external_ref"`
    ExpiresAt    *time.Time        `bun:"expires_at"`
    LastSyncedAt *time.Time        `bun:"last_synced_at"`
    CreatedAt    time.Time         `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt    time.Time         `bun:"updated_at,notnull,default:current_timestamp"`
}
```

Where `EntitlementsPayload` is the existing
`entitlements.Entitlements` type — minus the lifecycle/provenance fields
that are now break-out columns (`Source`, `ExternalRef`, `ExpiresAt`,
`LastSyncedAt`, `Metadata`) — wrapped with `Marshal`/`Unmarshal`
methods (or relying on bun's JSONB column tag, which already does this for
`JSONMap`). To keep the Go package layout clean (model package importing
service-layer struct), define the payload type in
`internal/db/models/entitlements_payload.go` and have the entitlements
service alias it.

```go
// In server/internal/db/models/entitlements_payload.go
type EntitlementsPayload struct {
    Version           int                `json:"version"`           // = 1
    Limits            EntitlementLimits  `json:"limits"`
    Features          EntitlementFeatures `json:"features"`
    AllowedCheckTypes []string           `json:"allowedCheckTypes,omitempty"`
    DisplayName       *string            `json:"displayName,omitempty"`
}

type EntitlementLimits struct {
    MaxChecks               *int `json:"maxChecks,omitempty"`
    MaxMembers              *int `json:"maxMembers,omitempty"`
    // ... etc.
}

type EntitlementFeatures struct {
    SSO            *bool `json:"sso,omitempty"`
    // ... etc.
}
```

The existing `entitlements.Limits` / `entitlements.Features` /
`entitlements.Entitlements` types become thin aliases over the
`models.Entitlements*` types, or are kept as the single source of truth
and imported by the model package. Either way, **one struct definition**
for the payload schema — pick the import direction at implementation
time, since both work and the choice is mostly cosmetic.

### `server/internal/entitlements/service.go`

- `merge()` collapses from ~70 lines to ~15: read `row.Payload`, copy
  into the resolver output, fall back to defaults via per-field
  `coalesce` (`if row.Payload.Limits.MaxChecks != nil` → use it, else
  default).
- `toModel()` collapses similarly: build the payload struct, set
  break-out fields directly.
- `defaultFeatureFlag()` becomes a reflect-driven lookup or a
  hand-written switch — but since the struct is now self-contained,
  consider replacing it with `Resolve().Features` and reading the field.
  (Out of scope for this spec; keep the switch and just update field
  paths.)

### `server/internal/db/postgres/postgres.go` and `…/sqlite/sqlite.go`

The UPSERT chain shrinks dramatically — only need to set:
`payload`, `metadata`, `source`, `external_ref`, `expires_at`,
`last_synced_at`, `updated_at`. Down from ~20 columns to 7.

### Tests

- `server/internal/entitlements/service_test.go` — adjust fixtures to
  build payload structs instead of setting individual fields. Logic-level
  tests (defaults merging, staleness, audit emission) shouldn't change.
- Add a new test: payload round-trip — write entitlements, read them
  back, assert struct equality.
- Add a migration test: seed an old-shape row in a test DB at the
  pre-018 migration state, run the migration, assert the new payload
  parses cleanly.
- The integration tests in `server/internal/handlers/entitlements/` go
  through the API contract and should pass unchanged — confirms the wire
  format is preserved.

### CLI / handlers

`server/internal/handlers/entitlements/handler.go` is unchanged — it
already speaks the `entitlements.Entitlements` JSON shape, which is
exactly what we now store. The handler↔service contract is preserved.

## Versioning policy

The `version` field in the payload starts at `1`. Future shape-breaking
changes (e.g. renaming `maxChecks` → `maxResourceChecks`) bump the version
and add a one-shot Go migration step that runs at unmarshal time:

```go
func (p *EntitlementsPayload) UnmarshalJSON(data []byte) error {
    var probe struct{ Version int `json:"version"` }
    _ = json.Unmarshal(data, &probe)

    switch probe.Version {
    case 0, 1:
        return p.unmarshalV1(data)
    default:
        return fmt.Errorf("unknown entitlements payload version %d", probe.Version)
    }
}
```

This is the same pattern already used for migrations of stored JSON
configs elsewhere in the codebase (see check config handling). Cheap,
explicit, easy to extend.

## Migration & rollout

1. **Single PR**, no feature flag — the model is six hours old and has
   zero stored rows; there is nothing to coexist with and nothing to
   backfill.
2. Rewrite the existing postgres migration `017_org_entitlements.up.sql`
   and sqlite `016_org_entitlements.up.sql` in place (and the matching
   `.down.sql` files). Anyone running a dev DB resets it via
   `SP_DB_RESET=true` or by deleting `solidping.db`.
3. Update the `OrgEntitlements` model and define `EntitlementsPayload`.
4. Update `merge`/`toModel` and the upsert SQL in both DB drivers.
5. Update tests.
6. Run `make lint test` until green.
7. Manual smoke: `./solidping serve`, hit
   `GET/PUT /api/v1/orgs/default/entitlements` round-trip with curl
   (token recipe in `server/CLAUDE.md`), confirm wire format unchanged.

## Risks

- **The `defaultFeatureFlag` switch in `service.go` keys on string
  literals**, not struct fields, and is used by `FeatureEnabled`. Make
  sure the new code path produces the same string keys (`"sso"`,
  `"mcp"`, etc.) so the API surface doesn't drift. The struct's `json`
  tags should match these literals exactly — they already do (verified).
- **bun's JSONB unmarshal into a typed struct silently ignores unknown
  keys**, which is what we want for forward-compat — but it also
  silently *drops* fields whose names changed. Mitigation: the `version`
  discriminator + an explicit `UnmarshalJSON` on `EntitlementsPayload`
  surfaces version mismatches loudly.
- **No queries break** — verified with grep that no SQL `WHERE` over the
  to-be-dropped columns exists in the codebase.

## Out of scope

- Indexing JSONB fields (e.g. `payload->'features'->>'sso'`). Add
  expression indexes only when an actual query needs them.
- Merging `payload` and `metadata` into a single column. They have
  different ownership (OSS vs. external pass-through) and the cost of
  keeping them separate is one extra column.
- Re-evaluating whether `display_name` should stay in the payload (it's
  display-only, no logic depends on it). Could be argued either way;
  this spec keeps it inside the payload for grouping purposes.

## Acceptance

- `make lint test` is green.
- `GET /api/v1/orgs/$org/entitlements` returns a response byte-equivalent
  (modulo timestamps) to a snapshot taken before this change.
- Adding a hypothetical new feature `feature_x` requires editing only
  `server/internal/db/models/entitlements_payload.go` (and tests, and the
  `defaultFeatureFlag` switch — *that* should perhaps be retired in a
  follow-up, but is not part of this refactor).
- The `2026-05-05-06-entitlements-model.md` spec's "Why columns and not
  a single JSONB blob" rationale is updated (in this spec's history
  section, not by editing the done spec) — column form was the right
  default, but the access pattern doesn't justify it.
