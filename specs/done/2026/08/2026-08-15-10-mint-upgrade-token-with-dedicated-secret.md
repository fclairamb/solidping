---
model: opus
effort: high
---

# The `#bt=` upgrade token is still signed with the static service bearer, so leaking that bearer forges plan changes for any org

## Problem

A leak of `entitlements.billing_inbound_secret` — a bearer credential that
travels on service calls — lets an attacker mint a valid upgrade token for
**any** org and drive its plan changes. This was identified in
[`2026-08-08-03`](../done/2026/08/2026-08-08-03-sign-billing-service-requests.md)
(§"One finding that belongs to the other side"), assigned to the billing repo,
and billing shipped the half it could ship alone. **The half that only this repo
can ship was never built**, so the vulnerability is still open.

### The two halves

**Billing side — done** (`solidping-billing`, spec `2026-08-08-01` part A). It
verifies against a dedicated `BILLING_UPGRADE_TOKEN_SECRET`, falls back to
`BILLING_INBOUND_SECRET`, gates the fallback behind
`BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET`, and logs on every fallback
acceptance:

> upgrade token accepted via the deprecated `BILLING_INBOUND_SECRET` fallback;
> **have the OSS mint with `BILLING_UPGRADE_TOKEN_SECRET`**, then set
> `BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET=false`

Its `billingtoken` package documents the counterpart as *"the shared secret the
OSS holds as the `entitlements.billing_upgrade_token_secret` system parameter"*.

**OSS side — never built.** That parameter does not exist in this repo. Grep for
`billing_upgrade_token_secret` returns nothing. The minter reads the bearer:

- [handler.go:56](../../../server/internal/handlers/entitlements/handler.go:56) —
  `ParamBillingInboundSecret = "entitlements.billing_inbound_secret"`, whose own
  doc comment says it is *"the shared HS256 secret used to sign the billing
  upgrade token"*. One value, two jobs.
- [handler.go:471](../../../server/internal/handlers/entitlements/handler.go:471)
  `adminUpgradeURL` → [handler.go:502](../../../server/internal/handlers/entitlements/handler.go:502)
  `billingInboundSecret` → [handler.go:527](../../../server/internal/handlers/entitlements/handler.go:527)
  `mintBillingToken(secret, …)`, appended as `#bt=` at
  [handler.go:497](../../../server/internal/handlers/entitlements/handler.go:497).
- [saas.go:22](../../../server/internal/app/saas.go:22) seeds only
  `SP_ENTITLEMENTS_BILLING_INBOUND_SECRET`; `SeedSaaSEntitlements`
  ([saas.go:43](../../../server/internal/app/saas.go:43)) has no upgrade-token
  branch.

So billing's fallback is not a deprecation window that is winding down — it is
**the only path in production**, and it will stay that way until this spec lands.
Its warning log fires on nothing today, because the OSS mints with the very
secret the fallback accepts (billing skips the warning when primary == legacy).

### Why it stalled (worth recording)

Each repo's spec assigned the work to the other. `2026-08-08-03` scoped itself to
"the solidping (OSS) side only" and wrote "the fix is billing-side (and is step 1
of the migration)". `2026-08-08-01` part A then implemented everything
implementable from billing alone — *accepting* a new secret. Minting with one is
the only piece that requires this repo, and no spec claimed it. From either
repo's changelog the split reads as done.

**Generalisable:** a cross-repo change where each side's spec assigns the other
side the work reads as complete from both sides. The sibling items — the
`X-SP-*` key sets and the legacy-bearer flip — deserve the same check against
code rather than against either changelog.

## Proposal

Mint with a dedicated secret, keeping the same fallback shape billing already
uses, so the two repos stay deployable in either order.

### 1. New system parameter

Add alongside `ParamBillingInboundSecret`:

```go
// ParamBillingUpgradeTokenSecret is the HS256 key that signs the `#bt=`
// upgrade token. It is deliberately NOT ParamBillingInboundSecret: that one
// is a bearer credential sent on every service call, and a leak of a bearer
// must not also be the power to mint an upgrade token for any org.
// Mirrors the billing service's BILLING_UPGRADE_TOKEN_SECRET.
ParamBillingUpgradeTokenSecret = "entitlements.billing_upgrade_token_secret"
```

### 2. Mint from it, falling back to the bearer while unset

`adminUpgradeURL` reads the new parameter first and falls back to
`billingInboundSecret` when it is empty. The fallback is what keeps a
self-hosted install and a not-yet-reconfigured SaaS working unchanged, and it
mirrors billing's verify-side fallback exactly — during the migration both ends
independently prefer the new secret and tolerate the old one, so **neither
deploy order breaks**.

Log at WARN, once per process (not per URL build — this is on a dashboard read
path), when the fallback is what produced the token: the operator-visible signal
that the split is still pending, mirroring billing's message.

### 3. Seed it

`SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET` → the new parameter, following
the existing seed pattern in `SeedSaaSEntitlements` (set only when the env var
is non-empty, so partial configuration stays fine and later API edits are not
clobbered on the next boot).

### 4. Reject the shared value in SaaS mode

If both parameters are set and **equal**, log an ERROR at boot naming the
vulnerability. Equal secrets are indistinguishable from the unsplit state, and a
deployment that believes it has split when it has not is worse than one that
knows it has not. Do not fail startup — a hard failure on a config value that is
"correct but not yet rotated" would strand an otherwise healthy prod boot.

### Non-goals

- Rotation of the upgrade-token secret (the ordered-key-set treatment
  `servicesig` gets). Overkill for a 1-hour-TTL token: rotating it costs at most
  one hour of stale `#bt=` links, and any user hitting one just reloads the
  dashboard.
- Adding an `aud` claim (raised and deferred in `2026-08-08-03` §147). Still
  deferred; independent of this change.
- Touching the static bearer's other job. It stays a bearer for legacy service
  calls, gated by `ParamAllowLegacyServiceToken` as today.

## Acceptance criteria

1. With `entitlements.billing_upgrade_token_secret` set, the `#bt=` token in the
   upgrade URL verifies against **that** secret and **fails** against
   `entitlements.billing_inbound_secret`.
2. With it unset, behaviour is byte-for-byte what it is today (token signed with
   the inbound secret) — no self-hosted or unconfigured install changes.
3. With it unset in SaaS mode, a WARN names the missing parameter, once.
4. With both set to the same value in SaaS mode, an ERROR at boot names the
   vulnerability. Startup still succeeds.
5. No billing secret at all → plain upgrade URL, no fragment (unchanged, pinned
   by `handler_test.go:380`).
6. Non-admins still get no `upgradeUrl` (unchanged, `handler_test.go:397`).
7. End-to-end against the real billing service: with the new secret set on both
   sides, an upgrade completes and billing logs **no** fallback warning.

## Migration (operator-facing, ordered)

1. Deploy this change. Nothing moves — the parameter is unset, the fallback
   mints exactly as before.
2. Generate one new secret. Set it on both sides:
   `SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET` here,
   `BILLING_UPGRADE_TOKEN_SECRET` there. Both ends prefer-new / accept-old, so
   the order does not matter and no restart has to be coordinated.
3. Confirm billing's fallback warning has stopped. Any token still arriving on
   the old secret means an OSS instance has not picked up the new value.
4. `BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET=false` on billing. The split is
   complete: the bearer can no longer mint anything.

Step 4 is what closes the vulnerability. Steps 1–3 only make it closeable —
worth stating plainly, because it is exactly the kind of migration that stops at
step 2 and is remembered as done.

## Implementation Plan

Scope: this repo only. Billing needs no code change — its half is already
deployed and waiting.

### 1. `server/internal/handlers/entitlements/handler.go`

- Add `ParamBillingUpgradeTokenSecret` next to `ParamBillingInboundSecret`
  ([:56](../../../server/internal/handlers/entitlements/handler.go:56)), with the
  comment explaining *why* it is separate — the next reader has to be able to
  see that collapsing them back is a security regression, not a simplification.
- Correct `ParamBillingInboundSecret`'s comment: it is a bearer, no longer the
  signing key.
- Extract `upgradeTokenSecret(ctx)` returning `(secret string, viaFallback bool,
  err error)`; `billingInboundSecret` stays as the fallback reader. Keep the
  parameter read generic — both go through `GetSystemParameter` with the same
  `param.Value["value"].(string)` shape.
- `adminUpgradeURL` ([:471](../../../server/internal/handlers/entitlements/handler.go:471))
  uses it. Signature of `mintBillingToken`
  ([:527](../../../server/internal/handlers/entitlements/handler.go:527)) is
  unchanged — it already takes the secret as an argument, which is what makes
  this a small change.
- `sync.Once`-guarded WARN on the fallback path.

### 2. `server/internal/app/saas.go`

- `envEntitlementsUpgradeTokenSecret = "SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET"`
  ([:22](../../../server/internal/app/saas.go:22) area).
- Seed branch in `SeedSaaSEntitlements`
  ([:43](../../../server/internal/app/saas.go:43)), `secret=true` like the other
  credential seeds.
- Equal-secrets ERROR check after both seeds have run.

### 3. Tests — `handler_test.go`

The point of the spec is the negative, so assert it directly:

- Token minted with the new secret **does not verify** against the inbound
  secret. This is the regression test that would have caught the original bug.
- Round-trip verifies against the new secret; claims unchanged
  (`purpose`/`org`/`sub`/`email`/`exp = iat + 1h`).
- Unset → falls back, still verifies against the inbound secret (parity with
  today's `handler_test.go:329`).
- Neither set → no `#bt=` fragment.
- Cross-org: a token minted for org A does not carry org B's slug (billing
  enforces the match, but the claim has to be right on this side).
- Seeding: env var absent leaves the parameter untouched; present upserts it.

### 4. Docs

- `wiki/api-specification/` entitlements page and any deployment/config table
  listing `SP_ENTITLEMENTS_*`: add the new var, and state that the inbound
  secret is a bearer only.
- `CLAUDE.md` SaaS/entitlements section: record the four-step migration above so
  the operator sequence is not only in this spec file.

## Related

- [`2026-08-08-03`](../done/2026/08/2026-08-08-03-sign-billing-service-requests.md)
  — found the vulnerability, assigned it away, scoped itself out.
- [`2026-07-11-14`](../done/2026/07/2026-07-11-14-signed-billing-upgrade-token.md)
  — introduced minting with the inbound secret (the state this changes).
- `solidping-billing` `2026-08-08-01` part A — the verify half, already shipped.
- `solidping-billing` `2026-07-11-03` — the upgrade token itself.
