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

`usage` fields:

| Field | Meaning |
|---|---|
| `usage.checks` | Non-internal, non-deleted checks. |
| `usage.checksPerMinute` | Aggregate check-execution rate. |
| `usage.ssoUsers` | Total member count (enforced against `limits.maxUsers`). |
| `usage.agents` | Active deported (private-location) agents (enforced against `limits.maxDeportedAgents`). |
| `usage.customDomains` | Live status pages with a custom domain set (enforced against `limits.maxCustomDomains`). |

### PUT /api/v1/orgs/:org/entitlements
Replaces the entitlement row. Returns the resolved entitlements.

Auth, in order of preference:

1. **A signed request** (the supported service path). The billing service signs
   with HMAC-SHA256 over
   `<timestamp>.<METHOD>.<path>.<hex sha256 of the raw body>` and sends:

   | Header | Value |
   |---|---|
   | `X-SP-Signature` | `v1,<base64 HMAC>` |
   | `X-SP-Timestamp` | Unix seconds (part of the signed string) |
   | `X-SP-Key-Id` | Which key of `entitlements.service_signing_keys` signed it |

   The `ServiceSignature` middleware (`internal/middleware/auth.go`, scheme in
   `internal/servicesig`) verifies it ahead of the normal `RequireAuth` chain
   and marks the request service-authorized, so cross-org writes work exactly
   as they did with the static bearer. Rejections — unknown key id, clock skew
   over 300s, signature mismatch — all return one generic 401; the reason is
   logged only.
2. **LEGACY**: a plain `Authorization: Bearer <entitlements.service_token>`,
   accepted only while `entitlements.allow_legacy_service_token` is true (its
   default) and logged as deprecated on every use. It is a shared secret, not a
   JWT, which is why it needs the `ServiceTokenBypass` middleware.
3. **An org admin JWT**, when `entitlements.admin_writes_enabled` is true (the
   default in self-hosted).

There is no `X-Entitlement-Token` header.

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
| `limits.maxDeportedAgents` | int / null | null ⇒ unlimited; caps active deported (private-location) agents across all private regions |
| `limits.maxCustomDomains` | int / null | null ⇒ unlimited; caps status pages served on a customer-owned domain. Only the none→some transition is gated (soft cap: dropping below the cap keeps existing custom domains working) |
| `limits.maxSmsPerMonth` | int / null | null ⇒ unlimited; caps outbound SMS per UTC calendar month |
| `limits.maxCallsPerMonth` | int / null | null ⇒ unlimited; caps outbound voice calls per UTC calendar month |
| `limits.maxWhatsappPerMonth` | int / null | null ⇒ unlimited; caps outbound WhatsApp template messages per UTC calendar month |
| `limits.maxSlos` | int / null | null ⇒ unlimited; caps service-level objectives |
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

- `entitlements.service_signing_keys` — ordered JSON array of
  `{"id","secret"}` (newest first) used to **verify** signed requests from the
  billing service. The signer uses the first entry; verification accepts any,
  which is what makes rotation overlap-safe. Mirrors the billing service's
  `BILLING_SIGNING_KEYS_OUTBOUND`.
- `entitlements.outbound_signing_keys` — the same shape, used to **sign** our
  own calls to the billing service's `/api/v1/*` endpoints. Mirrors its
  `BILLING_SIGNING_KEYS_INBOUND`. One set per direction, so a leak of one
  cannot forge the other.
- `entitlements.allow_legacy_service_token` — boolean, **default true**:
  whether the legacy static bearer below is still accepted. Flip it to false
  only once the billing service has stopped sending it; it is a parameter
  change, not a deploy.
- `entitlements.service_token` — LEGACY secret bearer token for the billing
  service. Unset in self-hosted by default. Superseded by the signing keys.
- `entitlements.admin_writes_enabled` — boolean, default true in
  self-hosted, set to false in SaaS to lock writes to the service token.
- `entitlements.upgrade_url_template` — template URL with `{org}` placed
  for the slug; surfaced on GET as `upgradeUrl` so the frontend can
  render an upgrade affordance. Empty in self-hosted.
- `entitlements.billing_inbound_secret` — the shared **bearer** credential
  presented on service calls between this instance and the billing service
  (its `BILLING_INBOUND_SECRET`). It is a bearer **only**. It is still
  accepted as the upgrade-token signing key while
  `entitlements.billing_upgrade_token_secret` is unset, purely so an
  unconfigured install keeps working. Seeded from
  `SP_ENTITLEMENTS_BILLING_INBOUND_SECRET`.
- `entitlements.billing_upgrade_token_secret` — the dedicated HS256 key that
  signs the `#bt=` upgrade token appended to `upgradeUrl`. Mirrors the billing
  service's `BILLING_UPGRADE_TOKEN_SECRET`; seeded from
  `SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`. Deliberately **not** the
  inbound bearer: a bearer travels on every service call, and leaking it must
  not also be the power to mint an upgrade token for any org. While this
  parameter is unset the minter falls back to the bearer and logs a WARN once
  per process. If both are set to the *same* value, boot logs an ERROR (and
  still starts) — equal secrets are indistinguishable from the unsplit state.
- `entitlements.stale_after_days` — days before a billing-service row is
  considered stale and the resolver falls back to defaults. Default 0
  (no stale fallback) in self-hosted.
