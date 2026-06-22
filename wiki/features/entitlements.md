# Entitlements

Per-org limits and feature toggles, with a deliberate split between the
OSS code (which knows raw numbers and booleans) and an external billing
service (which knows plans, prices, trials, and invoices).

The OSS never has to model "you're on the Pro plan, so you get…". It
stores the *result* — `maxChecks: 50`, `mcp: true` — and enforces those
numbers at the create boundaries.

## Where it lives

- **Resolver and merge**: [`server/internal/entitlements/`](../../server/internal/entitlements/)
- **HTTP handlers** (GET / PUT / PATCH / audit list): [`server/internal/handlers/entitlements/handler.go`](../../server/internal/handlers/entitlements/handler.go)
- **Database row**: `org_entitlements` — one row per org, JSONB `payload` column carrying limits, features, and source. Audit log in `org_entitlement_audits`.
- **Defaults seed**: [`server/internal/entitlements/defaults.go`](../../server/internal/entitlements/defaults.go) — the in-code defaults applied when a row is missing or has a `nil` field.

## Two halves: limits and features

`Limits` are quantitative caps. Each field is a `*int`; `nil` means
**unlimited**.

| Field | Default | Meaning |
|---|---|---|
| `maxChecks` | nil (unlimited) | Number of non-deleted checks the org can hold. |
| `maxMembers` | nil | User memberships. |
| `maxStatusPages` | nil | Public status pages. |
| `maxCheckGroups` | nil | Logical check groupings. |
| `maxMaintenanceWindows` | nil | Concurrently-stored maintenance windows. |
| `maxConnections` | nil | Notification channels (DB calls them `integration_connections`). |
| `maxWorkers` | nil | Self-registered worker count. |
| `maxApiTokens` | nil | Personal access tokens per user. |
| `retentionDaysRaw` | 30 | Days before raw results roll up. |
| `retentionDaysAggregated` | 365 | Days before aggregated results are pruned. |
| `minCheckPeriodSeconds` | 30 | Floor on check polling frequency. |

`Features` are boolean toggles. Each field is a `*bool`; `nil` means
"use the in-code default".

| Field | OSS default | Notes |
|---|---|---|
| `sso` | true | OAuth provider availability. |
| `mcp` | true | Model Context Protocol endpoint. |
| `customBranding` | true | Status-page custom logo / colors. |
| `prioritySupport` | false | Marketing flag, not enforced anywhere code-side. |
| `multiRegion` | true | Multi-region check workers. |
| `advancedAlerts` | true | Group incidents, cascade rollup, escalation policies. |

`AllowedCheckTypes` is a separate `[]string`. Empty list = all check
types allowed; a non-empty list restricts the org to a subset (used to
gate browser checks behind a higher-tier plan, for example).

## Three-layer resolution

A request to `GET /api/v1/orgs/$org/entitlements` (or any internal
caller) goes through `Service.Resolve`
([`entitlements/service.go:82`](../../server/internal/entitlements/service.go)).
Three things compose:

1. **Defaults** from `DefaultEntitlements` (the in-code seed).
2. **The org's stored row** from `org_entitlements`. Any non-nil field
   in `Limits` / `Features` overrides the default. `AllowedCheckTypes`
   replaces the default when non-empty.
3. **Live usage**, computed on every resolve. Counts of non-deleted
   checks, members, status pages, check groups, and connections so the
   dashboard can render "X / Y used" inline.

The resolved struct is what the API returns and what
`/api/v1/features` exposes to the dashboard. The stored row is *not*
returned directly — the resolver always merges defaults in first, so
external callers never see a nil-mean-default ambiguity.

## Sources

Every row records who wrote it. The `source` field discriminates the
write path:

| Source | Who writes it | Stale check applies? |
|---|---|---|
| `default` | The auto-create path when an org first calls Resolve and has no row. | no |
| `self-hosted` | The startup hook that establishes "this is a self-hosted instance, here are the local defaults". | no |
| `admin` | Manual override via `PUT /api/v1/orgs/$org/entitlements` from a logged-in admin (when admin writes are enabled). | no |
| `billing-service` | The external billing service writing via service-token auth. | yes |

## Stale fallback

A `billing-service` row carries `lastSyncedAt`. The resolver compares
this against `entitlements.stale_after_days` (system parameter,
default 0 = never stale). When the row is past its window, the API
response sets `stale: true`. Today this is informational — the
dashboard can show a "billing data outdated" banner — but the
limits remain in effect; we don't *unfreeze* limits when billing goes
silent.

The stale check applies **only** to billing-service rows. Admin
overrides are deliberate and persist; default and self-hosted rows
have no notion of staleness.

## Auth gating

The handler accepts two principals (service-token or admin user). Both
gates are configurable via system parameters so a self-hosted operator
can choose which path is open.

- **Service token** — set `entitlements.service_token` to a
  cryptographically-random opaque string and pass it in `X-Entitlement-Token`
  on PUT/PATCH calls. Compared with `subtle.ConstantTimeCompare`. This
  is how the SaaS billing service writes.
- **Admin user** — when `entitlements.admin_writes_enabled = true`, an
  authenticated org admin can also PUT/PATCH directly. Self-hosted
  operators turn this on; SaaS leaves it off so customers can't grant
  themselves the Enterprise tier.

GET reads use the standard auth surface (any authenticated org member).

## Audit log

Every write (PUT or PATCH) records a row in `org_entitlement_audits`
with the before-snapshot, after-snapshot, source, actor, and an
optional reason string. Service-token actors record as
`service:billing` (or whatever ID they self-identified as); admin
users record their UID. The list endpoint
(`GET /api/v1/orgs/$org/entitlements/audits`) returns paginated rows
for forensic trail; pagination uses `limit` (default 50, max 200).

## Enforcement

Limits are stored uniformly but enforced at write boundaries. The
`Service.CanCreate` hook is the integration point — handlers ask
"can I create another check?" before persisting. Today the package is
permissive (the v1 hook is a stub at
[`service.go:152`](../../server/internal/entitlements/service.go));
follow-up specs wire enforcement at each create-* boundary one by one
so each can be tested and rolled back independently.

Features are enforced at the use site, not at the storage layer:
`mcp: false` causes the MCP handler to refuse with 403; `multiRegion:
false` causes the worker context match to fall back to the default
region; etc.

## Wire format and storage

The on-disk shape is versioned. `EntitlementsPayload.Version = 1`
today; breaking shape changes bump the version and add a branch in
`UnmarshalJSON`
([`models/entitlements_payload.go:111`](../../server/internal/db/models/entitlements_payload.go)).
v0 (rows written before the version field landed) is treated as v1.
Forward-compat is unlimited for additive fields — unknown JSON keys
are silently ignored at unmarshal time.

The wire JSON is identical to the on-disk JSON. Renaming a field
breaks both surfaces; adding a field is safe in either direction.

## Common operations

```bash
# Read current entitlements
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:4000/api/v1/orgs/default/entitlements | jq .

# Admin overrides max checks (admin-writes must be enabled)
curl -s -X PATCH \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"limits":{"maxChecks":50}}' \
  http://localhost:4000/api/v1/orgs/default/entitlements

# Billing service replaces the entire row (PUT semantics)
curl -s -X PUT \
  -H 'X-Entitlement-Token: <opaque>' \
  -H 'Content-Type: application/json' \
  -d '{"source":"billing-service","limits":{...},"features":{...}}' \
  http://localhost:4000/api/v1/orgs/default/entitlements

# List the audit trail
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/entitlements/audits?limit=10' | jq .
```

## Origin

The entitlements model shipped in May 2026; design rationale and the
JSONB-collapse refactor are captured in:
- [`2026-05-05-06-entitlements-model.md`](../../specs/done/2026/05/2026-05-05-06-entitlements-model.md) — initial schema (broken-out columns).
- [`2026-05-05-16-entitlements-collapse-to-jsonb.md`](../../specs/done/2026/05/2026-05-05-16-entitlements-collapse-to-jsonb.md) — collapse to JSONB payload with versioning.
