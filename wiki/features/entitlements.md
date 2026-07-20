# Entitlements

Per-org quantitative limits, with a deliberate split between the OSS code
(which knows only raw numbers) and an external billing service (which knows
plans, prices, trials, and invoices).

The OSS never models "you're on the Pro plan, so you get…". It stores the
*result* — `maxChecks: 50` — and enforces it at the write boundaries.

> **There are no feature toggles.** Entitlements carry limits and display-only
> plan identity, nothing else. Feature gating is not done through this
> subsystem. (Earlier revisions of this page described a boolean `features` map
> and an `allowedCheckTypes` list; neither has ever existed in the code, and the
> API actively rejects them — see [Wire format](#wire-format-and-storage).)

## Where it lives

- **Resolver, usage, enforcement**: [`server/internal/entitlements/`](../../server/internal/entitlements/)
- **HTTP handlers** (GET / PUT / PATCH / audit list): [`handlers/entitlements/handler.go`](../../server/internal/handlers/entitlements/handler.go)
- **Payload model**: [`db/models/entitlements_payload.go`](../../server/internal/db/models/entitlements_payload.go)
- **Database**: `org_entitlements` — one row per org, JSONB `payload`. Audit log in `org_entitlement_audits`.
- **Defaults seed**: [`entitlements/defaults.go`](../../server/internal/entitlements/defaults.go)

## Limits

`EntitlementLimits` carries exactly three fields. Each is a `*int`; `nil` means
**unlimited**.

| Field | Meaning | Enforced at |
|---|---|---|
| `maxChecks` | Non-internal, non-deleted checks the org may hold. | `CheckCreateAllowed` → [`checks/service.go:946,3062`](../../server/internal/handlers/checks/service.go) |
| `maxUsers` | Total org members, however they joined. | `CheckMembership` → [`auth/service.go:413`](../../server/internal/handlers/auth/service.go) |
| `maxChecksPerMinute` | Aggregate check-execution rate (token bucket). | `ReserveCheckExecution` → [`checkworker/worker.go:877`](../../server/internal/checkworker/worker.go), [`agentws/handler.go:502`](../../server/internal/handlers/agentws/handler.go) |

`maxUsers` was renamed from `maxSsoUsers` (spec `2026-07-12-02`). The old key
survives as a **decode-only alias**; the payload always re-marshals as
`maxUsers`, and sending both keys at once is rejected with
`ErrConflictingUserLimitKeys`.

Breaches return a `QuotaError` carrying `LimitName`, `Limit` and
`CurrentUsage`. Both `CheckCreateAllowed` and `CheckMembership` count and then
insert non-atomically, so a tight race can slip one item past the cap — an
accepted trade-off for a soft quota guard, documented in the source.

Note the enforcement path covers **deported agents** as well as in-cluster
workers: `agentws` reserves rate-limit tokens on the agent dispatch path too, so
a private location cannot be used to bypass `maxChecksPerMinute`. See
[deported-agents.md](deported-agents.md).

### Defaults by deployment mode

`DefaultsFor(mode)` — anything not listed is `nil` (unlimited):

| Mode | maxChecks | maxUsers | maxChecksPerMinute | Display identity |
|---|---|---|---|---|
| Self-hosted | unlimited | 30 | unlimited | 🏠 Self-hosted |
| SaaS | 100 | 5 | 6 | 🆓 Free |

The SaaS numbers implement the Free tier of the 2026-07-12 pricing decision and
**must stay in sync** with `solidping-billing`'s Free SKU — they are the
"billing service has not reconciled us yet" fallback for a fresh org, and must
render and enforce identically to the real Free plan until billing writes its
own row. An unknown mode logs a warning and falls back to self-hosted defaults
rather than booting unbounded.

## Display identity

Alongside limits, the payload carries `displayName` and `displayEmoji`
(e.g. "🚀 Team"), supplied by the billing service and shown on the org **Usage**
page. These are **display-only and never enforced**. When a row has none of its
own, the mode defaults above apply — self-hosted deliberately gets a plain
"Self-hosted" label so it never claims to be "Free".

## Resolution

`GET /api/v1/orgs/:org/entitlements` (and every internal caller) goes through
`Service.Resolve` ([`entitlements/service.go:83`](../../server/internal/entitlements/service.go)),
composing three things:

1. **Defaults** for the deployment mode.
2. **The org's stored row** — any non-nil field overrides the default.
3. **Live usage**, recomputed on every resolve.

The resolver always merges defaults in first, so external callers never see a
nil-means-default ambiguity.

`Usage` has three fields:

| Field | Meaning |
|---|---|
| `checks` | Non-internal, non-deleted checks. System-created checks neither consume nor are gated by quota. |
| `checksPerMinute` | Aggregate execution rate derived from per-check periods. |
| `ssoUsers` | Total member count. **The wire key stays `ssoUsers` for back-compat** even though it is enforced against `maxUsers`. |

## Sources

Every row records who wrote it:

| Source | Who writes it | Stale check applies? |
|---|---|---|
| `default` | Auto-create path when an org first resolves with no row. | no |
| `self-hosted` | Startup hook establishing local defaults. | no |
| `admin` | Manual override via PUT/PATCH from an org admin (when admin writes are enabled). | no |
| `billing-service` | External billing service via service-token auth. | yes |

## Stale fallback

A `billing-service` row carries `lastSyncedAt`, compared against the
`entitlements.stale_after_days` system parameter (default 0 = never stale).
Past the window the API response sets `stale: true`. This is **informational** —
limits remain in effect. We do not *unfreeze* limits when billing goes silent.

The stale check applies only to billing-service rows; admin overrides are
deliberate and persist.

## Auth gating

Two principals can write, both governed by system parameters:

- **Service token** — set `entitlements.service_token` to a random opaque
  string and send it as a normal **`Authorization: Bearer <token>`**. The
  `ServiceTokenBypass` middleware ([`middleware/auth.go:211`](../../server/internal/middleware/auth.go))
  compares it with `subtle.ConstantTimeCompare` and, on match, marks the request
  service-authorized so the following `RequireAuth` + `RequireOrgAccess` become
  no-ops. This is how the SaaS billing service writes. It is a shared secret,
  not a JWT — which is why the route needs the bypass rather than the normal
  auth chain.
- **Admin user** — when `entitlements.admin_writes_enabled` is true (default in
  self-hosted), an authenticated org admin may PUT/PATCH directly. SaaS leaves
  it off so customers cannot grant themselves a higher tier.

GET reads use the standard auth surface (any authenticated org member).

System parameters: `entitlements.service_token`,
`entitlements.admin_writes_enabled`, `entitlements.upgrade_url_template`,
`entitlements.stale_after_days`, `entitlements.billing_inbound_secret`.

## Audit log

Every write records a row in `org_entitlement_audits` with before/after
snapshots, source, actor, and an optional reason taken from the
**`X-Entitlements-Reason`** request header. The list endpoint
(`GET …/entitlements/audits`) paginates with `limit` (default 50, max 200).

## Wire format and storage

The on-disk shape equals the wire shape. `EntitlementsPayloadVersion = 1`;
breaking changes bump the version and branch in `UnmarshalJSON`. v0 rows
(written before the version field) are treated as v1.

The complete payload is:

```json
{
  "version": 1,
  "source": "billing-service",
  "limits": { "maxChecks": 100, "maxUsers": 5, "maxChecksPerMinute": 6 },
  "displayName": "Team",
  "displayEmoji": "🚀"
}
```

**Two different strictness rules apply, and the distinction matters:**

- **At storage unmarshal**, unknown keys are silently ignored for
  forward-compatibility.
- **At the HTTP handler**, the request body is decoded with
  `DisallowUnknownFields` ([`handler.go:180`](../../server/internal/handlers/entitlements/handler.go)),
  so an unmodeled key is a hard **400** — it is not ignored. Sending `features`
  or `allowedCheckTypes` fails the entire request.

Accepted request keys: `limits` (`maxChecks`, `maxUsers`, `maxChecksPerMinute`,
`maxSsoUsers`), `source`, `displayName`, `displayEmoji`, `externalRef`,
`metadata`, `expiresAt`, `lastSyncedAt`.

> **PATCH quirk.** `mergePartial` seeds the outgoing row from the current
> resolved values for `Limits`, `DisplayName`, `DisplayEmoji`, `ExpiresAt` and
> `LastSyncedAt` — but **not** for `ExternalRef` or `Metadata`. A PATCH that
> omits `externalRef` therefore **drops** it. Send those fields explicitly, or
> use PUT.

## Common operations

```bash
# Read current entitlements (any org member)
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:4000/api/v1/orgs/default/entitlements | jq .

# Admin raises the check cap (admin writes must be enabled)
curl -s -X PATCH \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'X-Entitlements-Reason: manual bump for load test' \
  -d '{"limits":{"maxChecks":50}}' \
  http://localhost:4000/api/v1/orgs/default/entitlements

# Billing service replaces the whole row (service token is a plain bearer)
curl -s -X PUT \
  -H "Authorization: Bearer $SP_ENTITLEMENTS_SERVICE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"source":"billing-service","limits":{"maxChecks":100,"maxUsers":5},"displayName":"Team","displayEmoji":"🚀"}' \
  http://localhost:4000/api/v1/orgs/default/entitlements

# Audit trail
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/entitlements/audits?limit=10' | jq .
```

## Origin

- [`2026-05-05-06-entitlements-model.md`](../../specs/done/2026/05/2026-05-05-06-entitlements-model.md) — initial schema (broken-out columns).
- [`2026-05-05-16-entitlements-collapse-to-jsonb.md`](../../specs/done/2026/05/2026-05-05-16-entitlements-collapse-to-jsonb.md) — collapse to a versioned JSONB payload.
- `2026-07-12-02` — `maxSsoUsers` → `maxUsers` rename.

See also: [API reference — Entitlements](../api-specification/entitlements.md),
[database-model/entitlements.md](../database-model/entitlements.md).
