# Entitlements model — open-core foundation for SaaS billing

## Context

SolidPing is shipped as an open-source self-hostable monitoring product, and
will also run as a hosted SaaS at `solidping.io`. The hosted offering needs
plan-based limits (max checks, max members, SSO toggle, etc.) driven by
billing state managed in a separate service (see the sibling
`solidping-billing` repo).

We do **not** want billing code, Stripe SDK, plan SKUs, or trial logic to live
inside the OSS server. The OSS already needs per-org quotas regardless —
self-hosted operators want "cap this tenant at 100 checks" too. So the right
abstraction is: **the OSS owns a generic per-org entitlements model and
enforces it; an external caller (the billing service in SaaS, an admin in
self-hosted) writes to it via a privileged API.**

The OSS knows nothing about plan names, prices, trials, or invoices. It
stores raw numbers and booleans and enforces them. That is the entire scope.

## Goals

- One per-org `entitlements` record describing what the org is allowed to do.
- Single privileged HTTP endpoint for an external caller to push entitlements.
- One enforcement package called from every "create" handler (checks, members,
  status pages, …) — no scattered limit logic.
- Defaults that work out of the box for self-hosted (effectively unlimited).
- Graceful behavior when the external caller goes silent (stale → fall back).
- Frontend awareness: the existing `/api/v1/features` endpoint surfaces
  current entitlements *and* current usage so the UI can render
  "3 / 5 checks used" without a second roundtrip.

## Non-goals

- No plan vocabulary (`free`, `business`, `enterprise`) inside the OSS. The
  OSS has limits and features; plan names live in the billing service and
  may be passed through as a display-only string in `metadata`.
- No payment processing, Stripe webhooks, dunning, or invoicing.
- No trial logic. The billing service can express a trial by setting
  `expiresAt`; when it passes, the OSS falls back to defaults. That's it.
- No upgrade UI inside the OSS dashboard. A single `UPGRADE_URL` config
  value (empty in self-hosted, populated in SaaS) controls whether/where an
  "Upgrade" link appears.

## Data model

### Table: `org_entitlements`

One row per organization. Created lazily on first write (or backfilled at
migration time with defaults).

```sql
CREATE TABLE org_entitlements (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL UNIQUE REFERENCES organizations(uid)
                       ON DELETE CASCADE,

  -- Quantitative limits. NULL or -1 means unlimited.
  max_checks               integer,
  max_members              integer,
  max_status_pages         integer,
  max_check_groups         integer,
  max_maintenance_windows  integer,
  max_connections          integer,
  max_workers              integer,        -- self-registered workers
  max_api_tokens           integer,
  retention_days_raw       integer,        -- raw results retention
  retention_days_aggregated integer,       -- aggregated results retention
  min_check_period_seconds  integer,       -- floor on configurable period

  -- Boolean features. NULL means "use default".
  feature_sso              boolean,
  feature_mcp              boolean,
  feature_custom_branding  boolean,
  feature_priority_support boolean,         -- display-only, no enforcement
  feature_multi_region     boolean,
  feature_advanced_alerts  boolean,         -- escalation policies, on-call

  -- Per-check-type allowlist. NULL means all enabled types.
  allowed_check_types      text[],

  -- Provenance.
  source                   text NOT NULL CHECK (source IN
                             ('default','self-hosted','admin','billing-service')),
  display_name             text,            -- "Business plan" — for UI only
  external_ref             text,            -- billing-service customer id, opaque
  metadata                 jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- Lifecycle.
  expires_at               timestamptz,     -- nullable; e.g. trial end
  last_synced_at           timestamptz,     -- last write from external source
  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX org_entitlements_external_ref_idx
  ON org_entitlements (external_ref) WHERE external_ref IS NOT NULL;
```

**Why columns and not a single JSONB blob.** Type-checking, migration safety,
and `WHERE feature_sso = true` queries for analytics. JSONB only for
provider-opaque pass-through (`metadata`).

**Why `NULL = unlimited` for limits.** Lets a self-hosted org's defaults stay
genuinely unbounded without picking a magic sentinel that someone will
eventually reach. `-1` is also accepted on input for symmetry with REST
clients that struggle with explicit nulls.

### Table: `org_entitlement_audits`

One row per write. Cheap insurance against billing disputes ("you charged me
for the business plan but I never had SSO") and useful for debugging the
sync.

```sql
CREATE TABLE org_entitlement_audits (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  source            text NOT NULL,
  actor             text NOT NULL,           -- "billing-service", "user:<uid>"
  before_snapshot   jsonb,                   -- prior entitlements row, nullable
  after_snapshot    jsonb NOT NULL,          -- new entitlements row
  reason            text,                    -- caller-supplied free text
  created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX org_entitlement_audits_org_idx
  ON org_entitlement_audits (organization_uid, created_at DESC);
```

Retention: keep at least 90 days; configurable via system parameter. Pruning
job piggybacks on the existing aggregation/cleanup runner.

### Defaults

A single source of truth in code, e.g.
`server/internal/entitlements/defaults.go`:

```go
var DefaultEntitlements = Entitlements{
  MaxChecks:                 nil,  // unlimited
  MaxMembers:                nil,
  MaxStatusPages:            nil,
  MaxCheckGroups:            nil,
  MaxMaintenanceWindows:     nil,
  MaxConnections:            nil,
  MaxWorkers:                nil,
  MaxAPITokens:              nil,
  RetentionDaysRaw:          ptr(30),
  RetentionDaysAggregated:   ptr(365),
  MinCheckPeriodSeconds:     ptr(30),
  FeatureSSO:                ptr(true),
  FeatureMCP:                ptr(true),
  FeatureCustomBranding:     ptr(true),
  FeaturePrioritySupport:    ptr(false),
  FeatureMultiRegion:        ptr(true),
  FeatureAdvancedAlerts:     ptr(true),
  AllowedCheckTypes:         nil,  // all types
  Source:                    "default",
}
```

Self-hosted operators get this by default. They can override per-org via the
admin endpoint (e.g. to cap a noisy tenant). They can override the
*defaults* via system parameters (e.g. to lower retention globally).

In SaaS, the billing service overwrites `source = "billing-service"` and the
real plan limits land. Until then a new org sees the defaults — which on
the SaaS deployment should be tighter; the deployment configures them via
system parameters at boot, e.g.:

```
SP_ENTITLEMENTS_DEFAULTS_MAX_CHECKS=5
SP_ENTITLEMENTS_DEFAULTS_FEATURE_SSO=false
```

The system-parameter overlay is applied at startup to produce the in-memory
`DefaultEntitlements`. This way, the OSS code itself is identical between
self-hosted and SaaS — only env differs.

## API contract

### `GET /api/v1/orgs/$org/entitlements`

Returns the resolved entitlements for the org (DB row merged with defaults
for any null fields), plus a `usage` object computed live.

```json
{
  "limits": {
    "maxChecks": 100,
    "maxMembers": 10,
    "maxStatusPages": 3,
    "...": "..."
  },
  "features": {
    "sso": true,
    "mcp": true,
    "customBranding": false,
    "...": "..."
  },
  "allowedCheckTypes": null,
  "usage": {
    "checks": 27,
    "members": 4,
    "statusPages": 1
  },
  "source": "billing-service",
  "displayName": "Business",
  "expiresAt": "2026-12-31T23:59:59Z",
  "lastSyncedAt": "2026-05-05T08:14:22Z",
  "stale": false
}
```

Auth: any authenticated org member. Admins additionally see `metadata`,
`externalRef`, and the audit endpoint.

### `PUT /api/v1/orgs/$org/entitlements`

Replaces the entitlements row. Body shape mirrors the response (minus
`usage`, `stale`). Any field omitted is reset to `null` (i.e. "use
default"). This is intentional — partial updates are `PATCH`.

```http
PUT /api/v1/orgs/acme/entitlements
Authorization: Bearer <service-token>
X-Entitlements-Reason: "stripe.subscription.updated evt_1ABC..."

{
  "limits":   { "maxChecks": 100, "maxMembers": 10 },
  "features": { "sso": true, "mcp": true },
  "displayName": "Business",
  "externalRef": "cus_O...",
  "expiresAt":   null
}
```

Auth: **service token only** (see "Authentication" below). User JWTs cannot
write entitlements, even for admins, in SaaS mode. In self-hosted mode an
org admin *can* write directly — gated by a system parameter
(`entitlements.admin_writes_enabled`, default `true` in self-hosted, `false`
in SaaS).

### `PATCH /api/v1/orgs/$org/entitlements`

Same as `PUT` but only updates fields present in the body. Useful for
incremental changes (e.g. extend a trial).

### `GET /api/v1/orgs/$org/entitlements/audits`

Admin-only paginated audit log. Mirrors the shape of other list endpoints
(`{ data: [...], page: ... }`).

### Authentication for the billing service

A new system parameter: `entitlements.service_token` (secret). On bootstrap
the SaaS deployment sets it to a long random string shared with the billing
service. Requests carry it as `Authorization: Bearer <token>`. Middleware
recognises a token-shaped value matching this parameter and grants a
synthetic `service:entitlements` principal scoped to write-entitlements.

This keeps the existing user/org token model untouched and avoids inventing
a "system token" table for one caller. If a second service ever needs
similar access, generalise then.

The middleware MUST log the principal as `service:entitlements` (no leak of
the token) and MUST refuse the token on any route other than the
entitlements endpoints.

### Error codes

Add to `internal/handlers/base/`:

- `ErrorCodeEntitlementExceeded` — limit reached on a creation attempt.
  HTTP 403. Body includes `limit`, `currentUsage`, and `limitName` so the
  frontend can render a precise message.
- `ErrorCodeFeatureNotEntitled` — boolean feature is off. HTTP 403.
- `ErrorCodeEntitlementsStale` — only returned by the GET if the row is
  past its staleness window and the caller asked for raw data. Normal
  reads silently fall back to defaults (with `stale: true` in the response).

## Enforcement

A single package: `server/internal/entitlements/`.

```go
package entitlements

type Service struct { /* deps: db, defaults, clock */ }

// CanCreate is called from every create-* handler before the insert.
// resource is one of: "checks", "members", "status_pages", ...
func (s *Service) CanCreate(ctx context.Context, orgUID, resource string) error

// FeatureEnabled is called from handlers that gate behind a boolean.
func (s *Service) FeatureEnabled(ctx context.Context, orgUID, feature string) (bool, error)

// Resolve returns the full merged entitlements + live usage for an org.
// Used by the GET handler and /api/v1/features.
func (s *Service) Resolve(ctx context.Context, orgUID string) (Resolved, error)

// Set replaces the entitlements row. Writes an audit row in the same tx.
func (s *Service) Set(ctx context.Context, orgUID string, in Entitlements, actor, reason string) error
```

Wired into the existing `services.Registry`. Handlers call `CanCreate` /
`FeatureEnabled` at the top of their create / feature-gated paths.

### Race conditions

`CanCreate` performs a `SELECT count(*) WHERE organization_uid = ?` inside
the same transaction as the subsequent insert, with `SERIALIZABLE`
isolation **only for the count + insert** (not the whole handler). Cheaper
alternative if perf hurts: a `pg_advisory_xact_lock(hashtext(org_uid ||
resource))` around the count + insert.

For SQLite, the global write lock makes this a non-issue.

### Soft vs hard

If an org is already over a limit (typical after a downgrade), enforcement
must:

- **Allow** existing resources to continue functioning. Never disable a
  check or kick a user because they're over quota.
- **Block** new creates with `ENTITLEMENT_EXCEEDED`.
- **Surface** the over-limit state in `/api/v1/features` so the UI can
  show a banner.

This is the right behavior for the customer ("my paid stuff still works")
and the right behavior for us (clear upgrade prompt instead of broken
service).

### Per-resource cardinality

Quotas count **non-deleted** rows (`deleted_at IS NULL`). For checks:
both enabled and disabled count — disabling a check shouldn't be a quota
workaround.

## Frontend integration

`GET /api/v1/features` is extended to embed a slim `entitlements` block:

```json
{
  "bugReport": true,
  "entitlements": {
    "limits":  { "maxChecks": 100, "maxMembers": 10, "...": "..." },
    "features":{ "sso": true, "mcp": true, "...": "..." },
    "usage":   { "checks": 27, "members": 4, "statusPages": 1 },
    "displayName": "Business",
    "upgradeUrl": "https://solidping.io/upgrade?org=acme",
    "stale": false
  }
}
```

`upgradeUrl` is computed from a system parameter
`entitlements.upgrade_url_template` with `{org}` interpolation. Empty in
self-hosted → frontend hides upgrade affordances.

The frontend does **not** enforce limits — it only displays them and
disables CTAs. The server is the only authority. (Disabled CTAs are UX
courtesy, not security.)

## Self-hosted vs SaaS behavior

| Concern | Self-hosted default | SaaS default |
|---|---|---|
| `entitlements.admin_writes_enabled` | `true` (admin can edit via UI) | `false` (only billing service writes) |
| `entitlements.service_token` | unset (no external caller) | set at deploy time |
| `entitlements.upgrade_url_template` | unset (no upgrade button) | `https://solidping.io/upgrade?org={org}` |
| `entitlements.stale_after_days` | `0` (never stale) | `30` |
| Default limits | unlimited | tight free-tier values |

All of these are system parameters — the OSS code is identical, the
deployment differs.

## Stale entitlements

If `last_synced_at` is older than `entitlements.stale_after_days` AND
`source = 'billing-service'`, the org is considered stale. The
`Resolve` call falls back to `DefaultEntitlements` and marks `stale: true`
in the response.

This protects against "billing service forgets about an org and they keep
the business plan forever" without breaking self-hosted (where staleness
is disabled). A daily job logs stale orgs at `WARN` so operators can
investigate sync drift.

`source = 'admin'` overrides do **not** go stale — an admin override is a
deliberate intervention, not a sync.

## Migration

Three migrations, in order:

1. `01x_org_entitlements.up.sql` — create the two tables. No backfill in SQL.
2. App-level backfill on first boot after upgrade: for every existing org
   without a row, insert one with `source = 'default'` and all-null
   columns (the resolver merges with defaults at read time).
3. No code change to existing handlers in this migration step. The
   enforcement calls are added in a follow-up PR (one per handler) so the
   rollout is bisectable.

The down migrations drop the tables. Safe to run because no other table
references them.

## Implementation order

Roughly four PRs, each independently shippable:

1. **Foundation.** Tables, model, `entitlements.Service`, `Resolve`/`Set`,
   audit log, GET/PUT/PATCH endpoints, service-token middleware. No
   enforcement yet. Tests for the service and HTTP layer.
2. **Frontend exposure.** Extend `/api/v1/features` with the
   `entitlements` block. Frontend reads it, exposes via a hook
   (`useEntitlements()`), renders read-only "X / Y used" indicators on
   the relevant list pages.
3. **Enforcement, batch 1.** `CanCreate` calls in checks, members,
   status pages, check groups. `FeatureEnabled` gate on MCP and SSO.
4. **Enforcement, batch 2.** Remaining resources, allowed-check-types
   filter, soft-over-limit UX (banner), audits page in admin UI.

Each PR ships behind no flags — entitlements default to unlimited, so the
production behavior of the OSS doesn't change until someone actually
writes a tightening row.

## Out of scope

- The billing service itself (`solidping-billing` repo).
- Plan SKU → entitlements mapping (lives in billing service).
- Stripe webhooks, checkout, customer portal.
- Trial conversion logic. Trials are just rows with `expiresAt`; the
  billing service decides when to write a new row.
- Quota for time-series storage (raw vs aggregated retention is already a
  separate feature; we just expose the per-org override here).
- Rate limiting (req/min). Different problem, different layer (gateway).

## Test plan

- [ ] Unit: `entitlements.Resolve` merges defaults correctly with
      partial DB rows; null limits resolve to "unlimited"; stale orgs
      resolve to defaults.
- [ ] Unit: `CanCreate` blocks at the boundary, allows under the limit,
      respects soft-over-limit (existing rows unaffected).
- [ ] Unit: audit row is written in the same tx as the entitlements row;
      a failure on insert rolls back both.
- [ ] Integration: PUT with the service token writes; PUT with a user
      JWT is forbidden in SaaS mode; PUT with a user JWT (admin) works
      in self-hosted mode (`admin_writes_enabled`).
- [ ] Integration: race on `CanCreate` — two concurrent creates against
      `maxChecks = 1` produces exactly one success and one
      `ENTITLEMENT_EXCEEDED`.
- [ ] Integration: stale entitlements fall back to defaults; the response
      `stale` flag is `true`; a fresh PUT resets the stale state.
- [ ] Integration: feature gate (`feature_mcp = false`) returns
      `FEATURE_NOT_ENTITLED` from the MCP endpoint.
- [ ] E2E (dash0): "X / Y used" indicators render; CTA is disabled at
      cap; an "Upgrade" link appears only when `upgradeUrl` is non-empty.
- [ ] Manual: in self-hosted defaults, the entire UI looks identical to
      pre-entitlements behavior (no upgrade link, no quota warnings).

## Why this shape

A simpler design would have been: one `parameters`-table key per org
holding a JSON blob. Tempting because the parameters infra exists, but
it makes the audit log awkward, makes typed enforcement code uglier
(constant casting), and makes future analytics queries painful. A
dedicated table is the right cost.

A more elaborate design would model plans, plan-versions, and have the
OSS resolve `org → plan → entitlements`. That puts pricing-product
modeling inside the OSS, which is exactly what we're trying to avoid.
The OSS sees the resolved numbers and doesn't care where they came from.
