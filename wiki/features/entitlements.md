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

`EntitlementLimits` carries nine fields. Each is a `*int`; `nil` means
**unlimited**.

| Field | Meaning | Enforced at |
|---|---|---|
| `maxChecks` | Non-internal, non-deleted checks the org may hold. | `CheckCreateAllowed` → [`checks/service.go:946,3062`](../../server/internal/handlers/checks/service.go) |
| `maxUsers` | Total org members, however they joined. | `CheckMembership` → [`auth/service.go:413`](../../server/internal/handlers/auth/service.go) |
| `maxChecksPerMinute` | Aggregate check-execution rate (token bucket). | `ReserveCheckExecution` → [`checkworker/worker.go:877`](../../server/internal/checkworker/worker.go), [`agentws/handler.go:502`](../../server/internal/handlers/agentws/handler.go) |
| `maxDeportedAgents` | Active deported (private-location) agents across all private regions. | `AgentCreateAllowed` → [`agents/service.go` (`MintEnrollmentToken`)](../../server/internal/handlers/agents/service.go), [`agentws/handler.go` (`awaitEnroll`)](../../server/internal/handlers/agentws/handler.go) |
| `maxCustomDomains` | Status pages served on a customer-owned domain. | `CustomDomainAllowed` → [`entitlements/usage.go:144`](../../server/internal/entitlements/usage.go), called from [`statuspages/custom_domain.go:208-212`](../../server/internal/handlers/statuspages/custom_domain.go) |
| `maxSmsPerMonth` | Outbound SMS sent by the org per UTC calendar month. | notification dispatch (SMS channel) |
| `maxCallsPerMonth` | Outbound voice calls placed by the org per UTC calendar month. | notification dispatch (voice channel) |
| `maxWhatsappPerMonth` | Outbound WhatsApp template messages per UTC calendar month. | notification dispatch (WhatsApp channel) |
| `maxSlos` | Service-level objectives the org may hold. | `SloCreateAllowed` → [`entitlements/usage.go`](../../server/internal/entitlements/usage.go), called from [`slos/service.go` (`CreateSLO`)](../../server/internal/handlers/slos/service.go) |
| `whiteLabel` | **Boolean, not a cap.** Whether the org may drop the "powered by SolidPing" badge from its status pages (spec 2026-08-21-07). | `WhiteLabelAllowed` → [`entitlements/service.go`](../../server/internal/entitlements/service.go), called from [`statuspages/service.go`](../../server/internal/handlers/statuspages/service.go) |

`whiteLabel` is the one non-numeric entitlement, and its `nil` means something
different from every field above it: **`nil` = "use the deployment default"**,
not "unlimited" — a boolean has no unbounded reading. It is also only half of
the decision: the badge disappears only when the org is entitled AND the page
sets `hideBranding`. The resolver fails CLOSED (a lookup error keeps the badge),
because losing the badge when the plan does not include it is a silent revenue
leak while showing it for a moment is cosmetic.

`maxCustomDomains` is a **soft, one-directional gate**: only the transition
from "page has no custom domain" to "page has one" is checked against the
cap. Swapping an already-custom-domained page to a different domain is free
(it does not increase the count), and an org that drops below its cap (e.g.
after a downgrade) keeps its existing custom domains working — nothing is
revoked retroactively. Enforced at the domain editor's save path, which
renders a `402` as a quota alert
([`status-page-custom-domain.tsx:56,72,149`](../../web/dash0/src/components/shared/status-page-custom-domain.tsx)).

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

`maxDeportedAgents` is enforced twice: `MintEnrollmentToken` checks it first
for early UX (the dashboard can surface an upgrade prompt before the operator
ever starts a container), and `agentws`'s `awaitEnroll` checks it again at the
actual enrollment — the correctness point, since a token minted under the cap
could still over-enroll if the cap drops or another token is consumed first.
A rejection at enrollment time sends the agent a protocol `error` frame and
**does not consume the one-shot token**, so the same token can be retried
after an upgrade or after deleting another agent.

### Defaults by deployment mode

`DefaultsFor(mode)` — anything not listed is `nil` (unlimited):

| Mode | maxChecks | maxUsers | maxChecksPerMinute | maxDeportedAgents | maxCustomDomains | maxSmsPerMonth | maxCallsPerMonth | maxWhatsappPerMonth | maxSlos | whiteLabel | Display identity |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Self-hosted | unlimited | 30 | unlimited | unlimited | unlimited | unlimited | unlimited | unlimited | unlimited | **true** | 🏠 Self-hosted |
| SaaS | 100 | 5 | 10 | 1 | 0 | 0 | 0 | 0 | 2 | **false** | 🆓 Free |

Self-hosted gets `whiteLabel` unconditionally: an operator running their own
instance should never have to pay to take our badge off their own status page.

Self-hosted's unlimited `maxDeportedAgents` preserves the "free private
locations" competitive positioning (see
[deported-agents.md](deported-agents.md#competitive-position)). SaaS's `1`
mirrors the Free SKU of the plan ladder (Free 1, Starter 3, Pro 6, Scale 9).

SaaS's `maxCustomDomains`, `maxSmsPerMonth` and `maxCallsPerMonth` are all `0`
on the Free plan — none of the three ship on Free, and billing raises them per
paid plan. The plan ladder itself (which paid plan gets how many custom
domains, SMS, or calls) is owned by `solidping-billing`, not this repo — see
its `plans` package for the authoritative numbers.

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

`Usage` has seven fields:

| Field | Meaning |
|---|---|
| `checks` | Non-internal, non-deleted checks. System-created checks neither consume nor are gated by quota. |
| `checksPerMinute` | Aggregate execution rate derived from per-check periods. |
| `ssoUsers` | Total member count. **The wire key stays `ssoUsers` for back-compat** even though it is enforced against `maxUsers`. |
| `agents` | Active (non-revoked, non-deleted) deported agents across all private regions. Enforced against `maxDeportedAgents`. |
| `customDomains` | Live status pages with a custom domain set. Enforced against `maxCustomDomains` (soft, one-directional — see [Limits](#limits)). |
| `whatsappThisMonth` | Outbound WhatsApp template messages in the current UTC month. A persistent counter, not a live count. |
| `slos` | Live service-level objectives. Enforced against `maxSlos`. |

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

- **The billing service**, which **signs** each request (see below). The
  `ServiceSignature` middleware ([`middleware/auth.go`](../../server/internal/middleware/auth.go))
  verifies the signature and marks the request service-authorized, so the
  following `RequireAuth` + `RequireOrgAccess` become no-ops — entitlements
  writes are cross-org by design.
- **Admin user** — when `entitlements.admin_writes_enabled` is true (default in
  self-hosted), an authenticated org admin may PUT/PATCH directly. SaaS leaves
  it off so customers cannot grant themselves a higher tier.

GET reads use the standard auth surface (any authenticated org member).

### Signed service requests

The scheme lives in [`internal/servicesig`](../../server/internal/servicesig)
and is used in both directions between SolidPing and `solidping-billing`.
HMAC-SHA256 over the canonical string

```
<timestamp>.<METHOD>.<path>.<hex sha256 of the raw body>
```

carried as:

| Header | Value |
|---|---|
| `X-SP-Signature` | `v1,<base64 HMAC>` — versioned so a v2 can coexist |
| `X-SP-Timestamp` | Unix seconds, part of the signed string |
| `X-SP-Key-Id` | Which shared key signed it |

Because the body hash is signed, a captured request can neither be replayed
outside the **300s** skew window nor resent with a rewritten payload — the two
properties the static bearer never had. Rejections (unknown key id, skew,
mismatch — checked in that order, with a constant-time compare) are all one
generic 401; the reason goes to the log only. There is deliberately no nonce
cache: entitlement pushes are idempotent, so replaying an *identical* body is a
no-op, and body-binding is what actually matters.

**Two key sets, one per direction.** Each is an ordered JSON array of
`{"id","secret"}`, newest first: signers use the first entry, verifiers accept
any.

| Parameter | Direction | Billing-side mirror |
|---|---|---|
| `entitlements.service_signing_keys` | billing → SolidPing (entitlements push) | `BILLING_SIGNING_KEYS_OUTBOUND` |
| `entitlements.outbound_signing_keys` | SolidPing → billing (`/api/v1/*`) | `BILLING_SIGNING_KEYS_INBOUND` |

A leak of one direction's key therefore cannot be used to forge the other.

**Rotation** is: add the new key to the front of both sides' sets → both start
signing with it → drop the old entry. No lockstep restart, no window where
writes fail.

### Legacy static bearer (being retired)

`entitlements.service_token` is the original shared bearer. It is still
accepted while `entitlements.allow_legacy_service_token` is **true** (the
default), and every request authorized by it logs a `DEPRECATED` warning naming
the caller, so an operator can watch the legacy channel go quiet before
flipping the parameter to false. The migration order across the two repos is:

1. SolidPing verifies signatures, still accepting the legacy bearer *(here)*.
2. Billing starts signing, still sending the legacy bearer too.
3. SolidPing sets `allow_legacy_service_token=false` — a parameter flip, not a
   deploy, and reversible the same way.
4. Billing stops sending the bearer; `entitlements.service_token` becomes dead
   config and the `ServiceTokenBypass` middleware can be deleted.

System parameters: `entitlements.service_signing_keys`,
`entitlements.outbound_signing_keys`,
`entitlements.allow_legacy_service_token`, `entitlements.service_token`
(legacy), `entitlements.admin_writes_enabled`,
`entitlements.upgrade_url_template`, `entitlements.stale_after_days`,
`entitlements.billing_inbound_secret`,
`entitlements.billing_upgrade_token_secret`.

### The `#bt=` upgrade token has its own secret

`entitlements.billing_inbound_secret` (env
`SP_ENTITLEMENTS_BILLING_INBOUND_SECRET`) is a **bearer only** — a credential
that travels on service calls. The HS256 key that signs the `#bt=` upgrade
token appended to `upgradeUrl` is the separate
`entitlements.billing_upgrade_token_secret` (env
`SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`), mirroring the billing
service's `BILLING_UPGRADE_TOKEN_SECRET`.

They are deliberately distinct: leaking a bearer must not also be the power to
mint an upgrade token for **any** org. Collapsing them back into one value is a
security regression, not a simplification.

While the dedicated parameter is unset the minter falls back to the bearer —
that is what keeps a self-hosted install and a not-yet-reconfigured SaaS
working unchanged — and logs a WARN once per process (once, not per URL build:
this sits on a dashboard read path). If both parameters are set to the **same**
value, boot logs an ERROR naming the vulnerability and still starts; equal
secrets are indistinguishable from the unsplit state, but a hard failure on a
value that is merely unrotated would strand an otherwise healthy boot.

Operator migration (both ends prefer-new / accept-old, so deploy order does not
matter and no restart has to be coordinated):

1. Deploy. Nothing moves — the parameter is unset, the fallback mints as before.
2. Generate one new secret; set it on both sides
   (`SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET` here,
   `BILLING_UPGRADE_TOKEN_SECRET` on billing).
3. Confirm billing's fallback warning has stopped. Any token still arriving on
   the old secret means an instance has not picked up the new value.
4. Set `BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET=false` on billing.

**Step 4 is what closes the vulnerability.** Steps 1–3 only make it closeable —
worth stating plainly, because it is exactly the kind of migration that stops
at step 2 and is remembered as done.

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
  "limits": {
    "maxChecks": 100,
    "maxUsers": 5,
    "maxChecksPerMinute": 6,
    "maxDeportedAgents": 1,
    "maxCustomDomains": 0,
    "maxSmsPerMonth": 0,
    "maxCallsPerMonth": 0
  },
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
`maxDeportedAgents`, `maxCustomDomains`, `maxSmsPerMonth`, `maxCallsPerMonth`,
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

# Billing service replaces the whole row. Real callers SIGN the request
# (X-SP-Signature / X-SP-Timestamp / X-SP-Key-Id, see "Signed service
# requests" above); the legacy static bearer below still works only while
# entitlements.allow_legacy_service_token is true.
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
