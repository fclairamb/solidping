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
| `maxChecksPerMinute` | Per-process check-execution rate (token bucket). Internal checks are exempt. | `ReserveCheckExecution` → [`checkworker/worker.go`](../../server/internal/checkworker/worker.go) (`applyRateLimitGate`), [`agentws/handler.go`](../../server/internal/handlers/agentws/handler.go) (`handleClaim`) |
| `maxDeportedAgents` | Active deported (private-location) agents across all private regions. | `AgentCreateAllowed` → [`agents/service.go` (`MintEnrollmentToken`)](../../server/internal/handlers/agents/service.go), [`agentws/handler.go` (`awaitEnroll`)](../../server/internal/handlers/agentws/handler.go) |
| `maxCustomDomains` | Status pages served on a customer-owned domain. | `CustomDomainAllowed` → [`entitlements/usage.go:144`](../../server/internal/entitlements/usage.go), called from [`statuspages/custom_domain.go:208-212`](../../server/internal/handlers/statuspages/custom_domain.go) |
| `maxSmsPerMonth` | Outbound SMS sent by the org per UTC calendar month. | notification dispatch (SMS channel) |
| `maxCallsPerMonth` | Outbound voice calls placed by the org per UTC calendar month. | notification dispatch (voice channel) |
| `maxWhatsappPerMonth` | Outbound WhatsApp template messages per UTC calendar month. | notification dispatch (WhatsApp channel) |
| `maxSlos` | Service-level objectives the org may hold. | `SloCreateAllowed` → [`entitlements/usage.go`](../../server/internal/entitlements/usage.go), called from [`slos/service.go` (`CreateSLO`)](../../server/internal/handlers/slos/service.go) |
| `whiteLabel` | **Boolean, not a cap.** Whether the org may drop the "powered by SolidPing" badge from its status pages (spec 2026-08-21-07). | `WhiteLabelAllowed` → [`entitlements/service.go`](../../server/internal/entitlements/service.go), called from [`statuspages/service.go`](../../server/internal/handlers/statuspages/service.go) |

**An internal check counts nowhere, in all three systems** (spec
`2026-08-27-01`). `internal` marks server-created plumbing — the worker
self-stat checks written straight through `db.CreateCheck`, never through the
checks service — and it is not a writable request field on any path: create,
update, upsert and import/apply all refuse it with a `VALIDATION_ERROR`, and a
clone never inherits it. In exchange, the three accounting systems agree about
it: it is excluded from `maxChecks` (`ListOrgCheckRates` filters
`internal = FALSE`), absent from the checks-per-minute **demand** figure (same
filter), and — since this spec — exempt from both **rate gates**, so it draws no
token and never ticks the `check_rate_limited` skip counter. Before that last
piece, an internal check was unmetered by the quota, invisible to the predicted
demand, and yet competing for the org's real per-minute budget: the predictive
number and the factual skip counter could describe different fleets for the same
org. Both gates read the flag off the check row attached at claim time
(`checkjobsvc.attachChecks`) — there is no `internal` column on `check_jobs`.

*Upgrade note — check for residue, no migration ships.* Any check an org
created with `internal: true` BEFORE that spec is still exempt from `maxChecks`,
and nothing in the code can tell it apart from a server-created one after the
fact — except by slug, since the two legitimate creators use reserved prefixes:

```sql
SELECT o.slug AS org, c.slug, c.type, c.enabled, c.created_at
FROM checks c JOIN organizations o ON o.uid = c.organization_uid
WHERE c.internal = TRUE
  AND c.deleted_at IS NULL
  AND c.slug NOT LIKE 'int-checks-%'
  AND c.slug NOT LIKE 'int-jobs-%';
```

Expected: zero rows. If an install has some, clear the flag on those UIDs
(`UPDATE checks SET internal = FALSE WHERE uid IN (…)`) — after which they count
against `maxChecks` like any other check, which is the point. Deliberately not
automated: silently re-metering a customer's checks mid-migration is a
billing-visible change an operator should make on purpose.

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

`maxChecksPerMinute` has two properties worth knowing before reading a graph
(spec `2026-08-26-02`):

- **The bucket is per process, not per fleet.** `Service.limiterFor` keeps it
  in memory, so an org whose checks run in R regions has R independent buckets
  and can sustain up to `cap × R` executions per minute, even though the field
  reads as an aggregate org rate. Deliberate: it errs generous, never stingy,
  and needs no coordination on the hot path. Making the cap exact means a
  shared per-org reservation, not a smaller per-process cap.
- **A turned-away job is deferred, not dropped, and the deficit rotates.** The
  deferral advances `scheduled_at` but preserves `effective_scheduled_at`
  (`checkjobsvc.DeferLeaseRateLimited`), so this window's losers are next
  window's first pick. An over-cap org therefore degrades as "every check runs
  at roughly cap/demand of its configured rate", not as "half the checks run
  perfectly and the other half never run" — which is exactly how it failed
  before, since check phases are a stable hash of the check UID and never
  reshuffle on their own. Passive checks (heartbeat, email) make no outbound
  request and never consume a token.

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
2. **The org's stored row** — merged in one of two modes, see below.
3. **Live usage**, recomputed on every resolve.

The resolver always merges defaults in first, so external callers never see a
nil-means-default ambiguity.

### Two merge modes, decided by the row's source

| Row source | nil field means | Why |
|---|---|---|
| `billing-service`, `org-admin`, `self-hosted` | **not stated** → the deployment default shows through | Billing pushes a partial plan; everything the SKU is silent about must still get sane numbers. |
| `admin` (the superadmin editor) | **unlimited** | The editor pre-fills every cap from the resolved values and saves a *complete* row (spec 2026-08-26-06, "whole-row only"), so a nil there is a deliberate statement, not an omission. |

This distinction is load-bearing on SaaS, where **every default is non-nil**.
Under null-fill, a superadmin who flipped a cap to "Unlimited" would store
`null`, watch the SaaS default (100 checks, 10/min…) reappear on the way back
out, and see the toggle silently flip itself off — org still capped, UI
reporting the number it had just failed to change.

**One exception inside the exception:** `whiteLabel` is a boolean, so its nil
cannot mean "unbounded". It keeps null-fill semantics in *both* modes — nil
means "use the deployment default" — otherwise every admin override that only
touched numbers would silently revoke white-label on a deployment that grants
it by default.

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

## The over-limit banner (`checksPerMinute`)

An org that exceeds `maxChecksPerMinute` has its executions silently deferred by
the rate gate. Until spec 2026-08-26-03 the only traces were an INFO log line
and the `ChecksRateLimited` Prometheus counter — operator-facing, both of them.
What the customer saw was unexplained gaps in their results, which reads as "the
product is broken" (exactly how the 2026-08-26 incident presented).

The GET response therefore carries a `checksPerMinute` object **outside**
`?with=usage`:

| Field | Meaning |
|---|---|
| `demand` | Σ over enabled, non-deleted, non-internal, **non-passive** checks of `max(1, regions) × 60s/period`. |
| `limit` | Resolved `maxChecksPerMinute`; `null` = unlimited. |
| `skippedToday` | Executions the gate deferred today (UTC), from `org_usage_counters` (`kind = 'check_rate_limited'`). |

Three decisions worth keeping:

- **Org-level, never per-check.** Once the deferral rotates across the org's
  checks (spec 2026-08-26-02), a per-check flag lights up almost everywhere and
  carries no information. Per-check flags/badges were explicitly rejected.
- **`demand` excludes passive types** (heartbeat, email) while
  `usage.checksPerMinute` keeps counting them. Passive checks return before the
  gate (`checkworker/worker.go`), so they consume no execution budget and can
  never be why an org is throttled. The two figures answer different questions —
  "what does the gate meter?" versus "what has the org configured?".
- **`skippedToday` is a daily bucket, not monthly** like the SMS/voice/WhatsApp
  counters. Monthly would keep the banner lit for weeks after an org came back
  under its cap. It is written from **both** claim paths — the in-process worker
  gate and the agent dispatch gate in `handlers/agentws` — because an org
  running entirely on private locations is throttled by a gate that lives in the
  server, not in its agents.

`dash0` renders it as an amber warning (`CheckRateLimitBanner`) on the checks
list and the org Usage page whenever `demand > limit` (predictive) **or**
`skippedToday > 0` (factual — the org may have just dropped back under its cap
and still have holes in today's history).

## Sources

Every row records who wrote it:

| Source | Who writes it | Stale check applies? |
|---|---|---|
| `default` | Auto-create path when an org first resolves with no row. | no |
| `self-hosted` | Startup hook establishing local defaults. | no |
| `admin` | **Superadmin override.** Minted only by a superadmin — through the instance editor (`PUT /api/v1/system/entitlements/:org`) or by a superadmin calling the org-scoped route. Resolves whole-row, and **suppresses billing pushes** until released. | no |
| `org-admin` | Manual write via org-scoped PUT/PATCH from an org admin (when admin writes are enabled). Ordinary null-fill resolution; billing's next reconcile overwrites it. | no |
| `billing-service` | External billing service via signed (or legacy static-bearer) service auth. | yes |

### Why `admin` and `org-admin` are separate

The source is an **authorization outcome, not a label** — `admin` is what makes
a row outrank billing — so the server decides it and a request body never gets
to assert it (`Handler.sourceFor`).

The org-scoped PUT is open to any org admin whenever
`entitlements.admin_writes_enabled` is unset, since it **defaults to true**; on
SaaS that parameter is only written when `SP_ENTITLEMENTS_ADMIN_WRITES` is set
explicitly. Before the precedence rule that door was harmless — billing's next
reconcile corrected whatever it wrote. If such a write could mint `admin`, it
would instead be a permanent lockout: any org admin could grant themselves
limits *and* stop billing from ever correcting them. Hence the split. Both
sources carry the same paid plan weight, so self-hosted scheduling behaviour is
unchanged.

**Migration (v0.19.0): legacy `admin` rows are relabelled to `org-admin`.**

Every row that could hold `source = 'admin'` predates the superadmin editor —
until spec 2026-08-26-06 landed, *every* non-service write got `admin`: org
admins, self-hosted operators and the CLI alike. Such rows are routinely
**partial**, because the API has never required a complete payload on `PUT`.

Leaving them would have handed them **both** new powers of `admin`, and the
second one is the dangerous half:

- they would start suppressing billing pushes, and
- they would start resolving **whole-row**, so every cap they never mentioned
  would flip from the deployment default to **unlimited** — on SaaS
  `maxChecksPerMinute` 10 → ∞ (the very cap this feature exists to manage),
  `maxUsers` 5 → ∞, `maxSlos` 2 → ∞, and
  `maxSmsPerMonth`/`maxCallsPerMonth`/`maxWhatsappPerMonth` 0 → ∞, i.e.
  unbounded spend on the instance's own Twilio/Meta credentials. On
  self-hosted, where that door is the *normal* one because
  `entitlements.admin_writes_enabled` defaults to true, `maxUsers` 30 → ∞
  lifts the seat guard.

That escalation lands on the **first resolve after deploy**, long before any
operator opens the editor, so a release note would not have been a control.
Migration `016_v0_19_0` therefore relabels `payload.source` from `admin` to
`org-admin` in both dialects. It is semantically lossless: by construction
those rows *were* org-scoped writes, and `org-admin` is the old behaviour under
a new name.

**What an operator sees.** Nothing changes for a legacy row: same limits, same
paid plan weight, same null-fill resolution, still overwritten by billing's
next reconcile. Each relabelled org gains one audit entry (source
`migration:org-admin-relabel`) explaining the change, visible in the superadmin
editor. If a row was *meant* to be a genuine override, re-save it from
**Server → Entitlements** — one click — and it becomes `admin` again, this time
with the complete payload the whole-row reading assumes.

`org_entitlement_audits.source` is **not** rewritten by the migration. It is
the historical log of what each past write claimed, is read for display only,
and falsifying it to match today's vocabulary would be worse than the
vocabulary drifting. The audit row the migration inserts is what bridges the
two.

**Known footgun (pre-existing, deliberately unchanged).** A *trusted service*
may still name its own source, so the billing service — or anything holding the
legacy static bearer — can write `source: "admin"` and thereby suppress its own
future pushes until a superadmin releases the org. No caller does this today;
the marker comment sits on `Handler.sourceFor` if it ever needs closing.

### Superadmin override lifecycle

1. A superadmin writes limits → row becomes `admin`.
2. Billing pushes → **200, nothing applied**; the response carries
   `applied: false` and `suppressedBy: "admin"`, and an audit row is written
   with source `billing-service:suppressed` carrying the rejected payload.
   Billing answering 200 is deliberate: it must not error-loop over a decision
   we made on purpose.
3. A superadmin releases (`DELETE /api/v1/system/entitlements/:org`) → the row
   is **deleted** (audited as `admin:released`), the org resolves to the
   deployment defaults, and the next billing push applies normally.

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
