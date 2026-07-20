# Entitlements

Per-org numeric limits plus display-only plan identity. Owned by an external
billing service in SaaS, by org admins in self-hosted. The OSS knows nothing
about plan SKUs — only raw numbers and the display strings it is handed. NULL
on a limit means "unlimited".

See [`../features/entitlements.md`](../features/entitlements.md) for the
resolver, the defaults per deployment mode, and how the dashboard renders plan
identity and usage.

## Endpoints

### GET /api/v1/orgs/:org/entitlements
Returns the resolved entitlements (defaults merged with the stored row),
plus live `usage` counts and a `stale` flag, and `upgradeUrl` when
`entitlements.upgrade_url_template` is configured. Auth: any authenticated
org member.

### PUT /api/v1/orgs/:org/entitlements
Replaces the entitlement row. Returns the resolved entitlements.

Auth: a plain `Authorization: Bearer <entitlements.service_token>` (preferred
for SaaS) OR an org admin JWT when `entitlements.admin_writes_enabled` is true
(the default in self-hosted). The service token is a shared secret, **not** a
JWT — it is let through by the `ServiceTokenBypass` middleware
(`internal/middleware/auth.go`) ahead of the normal `RequireAuth` chain. There
is no `X-Entitlement-Token` header.

Optional `X-Entitlements-Reason` header is recorded on the audit log.

### PATCH /api/v1/orgs/:org/entitlements
Same auth and body as PUT, but merges over the currently resolved row instead
of replacing it. Useful for incremental changes (e.g. extend a trial). Returns
the resolved entitlements.

### GET /api/v1/orgs/:org/entitlements/audits
Returns the entitlement audit rows for the org, newest first. Optional
`?limit=` query parameter (default 50, max 200). Auth: org admin or
service token.

## Request body

The handler decodes with `DisallowUnknownFields`
(`server/internal/handlers/entitlements/handler.go`), so **any key outside the
list below is a hard `400`** — typos surface loudly instead of silently
no-op-ing.

| Field | Type | Notes |
|---|---|---|
| `limits.maxChecks` | int / null | null ⇒ unlimited |
| `limits.maxUsers` | int / null | null ⇒ unlimited |
| `limits.maxChecksPerMinute` | int / null | null ⇒ unlimited |
| `limits.maxSsoUsers` | int / null | **deprecated alias** for `maxUsers`; sending both is rejected (`ErrConflictingUserLimitKeys`) |
| `source` | string | defaults to `billing` for a service token, `admin` for an admin JWT |
| `displayName` | string | display-only plan name, e.g. `Team` |
| `displayEmoji` | string | display-only, e.g. `🚀` |
| `externalRef` | string | billing-side identifier |
| `metadata` | object | free-form billing-side payload |
| `expiresAt` | RFC3339 | |
| `lastSyncedAt` | RFC3339 | |

`features` and `allowedCheckTypes` are **not accepted** — sending either returns
`400 VALIDATION_ERROR`. There is no boolean-feature surface in this API.

Example:
```json
{
  "limits": { "maxChecks": 500, "maxChecksPerMinute": 120 },
  "displayName": "Team",
  "displayEmoji": "🚀",
  "externalRef": "cus_ABC123",
  "expiresAt": "2027-01-01T00:00:00Z"
}
```

### PATCH quirk: `externalRef` and `metadata` are not carried forward

`mergePartial` seeds the merged row from the current resolved entitlements for
`limits`, `displayName`, `displayEmoji`, `expiresAt`, and `lastSyncedAt` — but
it does **not** seed `externalRef` or `metadata`. Those two are only set when
present in the request body. A PATCH that omits `externalRef` therefore
**drops** the stored value (same for `metadata`). Billing integrations should
resend both on every PATCH, or use PUT.

## System parameters

- `entitlements.service_token` — secret bearer token for the billing
  service. Unset in self-hosted by default.
- `entitlements.admin_writes_enabled` — boolean, default true in
  self-hosted, set to false in SaaS to lock writes to the service token.
- `entitlements.upgrade_url_template` — template URL with `{org}` placed
  for the slug; surfaced on GET as `upgradeUrl` so the frontend can
  render an upgrade affordance. Empty in self-hosted.
- `entitlements.stale_after_days` — days before a billing-service row is
  considered stale and the resolver falls back to defaults. Default 0
  (no stale fallback) in self-hosted.
