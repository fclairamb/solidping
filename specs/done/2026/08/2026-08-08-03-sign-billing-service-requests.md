---
model: opus
effort: high
---

# Both service channels to solidping-billing ride on static bearer secrets — sign the requests instead

## Problem

There are three channels between this service and `solidping-billing`, and the
security properties are inverted: the one facing an untrusted browser is
signed, scoped and short-lived, while the one that can rewrite **any** org's
entitlements is a static forever-bearer.

### 1. Receiving entitlement changes (billing → OSS) — static bearer

`PUT`/`PATCH /api/v1/orgs/:org/entitlements` are authorized by
`ServiceTokenBypass(entitlements.service_token)`
([server.go:1099](server/internal/app/server.go:1099),
[auth.go:212-228](server/internal/middleware/auth.go:212)). The middleware
constant-time-compares the bearer against a system parameter and, on a match,
marks the request trusted so **`RequireAuth` and `RequireOrgAccess` become
no-ops** — by design, since billing writes cross-org.

That single static string is therefore a permanent, unscoped,
write-any-org credential, and nothing about the request is bound to it:

- **No expiry.** One leak — a reverse-proxy access log, a crash dump, shell
  history, billing's own `push_log` table, a screenshot — is a credential
  valid until somebody notices and coordinates a rotation.
- **No payload binding.** Capture one legitimate PUT and you can replay it
  verbatim indefinitely, or **swap the body** (`maxChecks: 100` →
  `maxChecks: 10000000`) and reuse the same header. The signature that would
  make body tampering detectable does not exist.
- **No timestamp**, so there is no replay window to enforce and no way to
  reject a request captured months ago.
- **Rotation is a coordinated outage.** The value lives in a system parameter
  here and in `BILLING_SOLIDPING_TOKEN` there; changing it means restarting
  both sides in lockstep, so in practice it never gets rotated.

### 2. Sending the customer to billing (OSS → browser → billing) — already signed

`adminUpgradeURL` appends an HS256 JWT as a URL fragment
([handler.go:414-441](server/internal/handlers/entitlements/handler.go:414)),
minted by `mintBillingToken`
([handler.go:470](server/internal/handlers/entitlements/handler.go:470)) with
`purpose=billing`, `org`, `sub`, `email`, `iat`, `exp=iat+1h`. Billing verifies
it statelessly and rejects a token scoped to a different org.

**This one is already right.** It is the model the other two should copy:
short-lived, purpose-scoped, org-bound, and carried in a fragment so it never
reaches a server log or `Referer` header.

### 3. Calling billing's server-to-server API (OSS → billing) — scheme exists, caller does not

`solidping-billing` exposes `/api/v1/checkout`, `/api/v1/portal` and
`/api/v1/plans` behind a static `BILLING_INBOUND_SECRET` bearer
(`internal/middleware/auth.go` in that repo) and its `CLAUDE.md` describes them
as "SolidPing UI (proxy)" endpoints.

**No such caller exists in this repository.** Grepping `server/internal` for an
outbound HTTP client to billing, and for any billing base-URL configuration,
finds only the upgrade-URL template and its tests — the browser calls billing's
`/api/public/*` directly instead. So this half of the work is *defining the
scheme before the first caller is written*, not retrofitting an existing one.
It is included because whoever writes that caller will otherwise reach for the
same static bearer by default.

## Proposal

Adopt **one** request-signing scheme for both service-to-service directions,
and leave the already-correct `#bt=` upgrade token alone.

The pair already depends on Standard Webhooks (billing verifies Polar webhooks
with `standard-webhooks/libraries/go`), so an HMAC-over-the-request scheme is
not new vocabulary for this system.

### The scheme

Sign a canonical string built from the parts an attacker must not be able to
change:

```
<timestamp>.<method>.<path>.<hex sha256 of raw body>
```

with HMAC-SHA256, sent as:

| Header | Value |
|---|---|
| `X-SP-Signature` | `v1,<base64 HMAC>` — versioned so v2 can coexist |
| `X-SP-Timestamp` | Unix seconds, part of the signed string |
| `X-SP-Key-Id` | Which shared key signed it (enables overlap during rotation) |

Verification rejects, in order: unknown/absent key id → clock skew over
**300s** → signature mismatch (constant-time compare). Every rejection is one
generic 401; the reason goes to the log, not the response.

Body hashing means the raw bytes must be read once, hashed, and handed to the
decoder — the middleware has to buffer the body rather than let the handler
consume the stream first.

### Replay

The timestamp window is the primary defense. A nonce cache is deliberately
**not** proposed for v1: entitlement pushes are idempotent and deterministic
(billing produces the same body for the same Polar state), so replaying an
identical body is a no-op. The property that actually matters is that a
replayed request can no longer carry a *different* body — which body-binding
gives us. Revisit if a non-idempotent signed endpoint ever appears.

### Key storage and rotation

Replace the single `entitlements.service_token` with
`entitlements.service_signing_keys`: a small JSON array of `{id, secret}`,
newest first. Signers use the first entry; verifiers accept any. Rotation
becomes: add a key to both sides → both start signing with it → drop the old
one. No lockstep restart, no window where writes fail.

### Migration (this is a wire contract — both repos must move together)

1. **Both sides accept both.** Land verification here behind
   `entitlements.allow_legacy_service_token` (default `true`); land signing in
   billing. Every request authorized by the legacy bearer logs a warning naming
   the caller, so operators can see when the legacy path has gone quiet.
2. **Both sides send signed.** Billing signs by default; the legacy bearer is
   still accepted but unused.
3. **Flip legacy off.** Set the parameter to `false` (and default it to `false`
   in a later release). `entitlements.service_token` is then dead config.

Rolling backwards at any step is a parameter flip, not a deploy.

### Per-direction work

**Receiving (the channel that matters today):** add a `ServiceSignature`
middleware next to `ServiceTokenBypass` on the entitlements group
([server.go:1098-1100](server/internal/app/server.go:1098)). It must set the
same `serviceAuthContextKey` so the downstream `RequireAuth` /
`RequireOrgAccess` no-op behavior is preserved — the goal is to change *how the
caller proves identity*, not what a proven service is allowed to do. Keep
`ServiceTokenBypass` in the chain until step 3, then delete it.

**Sending:** add the signer as a small reusable helper (it is the same HMAC in
the other direction) so the future checkout/portal proxy client is signed from
its first commit. Do not build the proxy client itself under this spec.

**Upgrade token:** unchanged. Optionally add an `aud` claim naming the billing
deployment, so a token minted by one OSS instance cannot be presented to a
different billing service that happens to share a secret — worth doing only if
multiple deployments ever share secrets.

## Acceptance criteria

- A `PUT /orgs/:org/entitlements` with a valid signature succeeds and is
  treated as service-authorized (cross-org write still works).
- The same request with one byte of the body changed → 401.
- The same request replayed with a timestamp older than the skew window → 401.
- A request signed with a key id absent from the parameter → 401.
- With `allow_legacy_service_token=true`, a legacy bearer request still
  succeeds and emits a deprecation warning; with `false`, it 401s.
- Rotation test: two keys configured, requests signed with either are accepted.
- No test asserts a static token in a header any more except the legacy-path
  tests.

## Open questions

- **Naming.** `X-SP-*` matches nothing else in this codebase; Standard Webhooks
  uses `webhook-id` / `webhook-timestamp` / `webhook-signature`. Reusing those
  header names would let billing share verification code with its Polar path,
  at the cost of implying this is a webhook when it is a plain API call. Pick
  one and use it in both repos.
- **Is `sub`/actor attribution wanted on the inbound push?** Today an
  entitlements write is attributed to "the billing service" as a whole. Signing
  does not change that, but if the audit trail should name which billing
  deployment wrote, the key id is the natural carrier.
- **Scope of the key.** One key for both directions, or one per direction? Two
  keys is marginally better hygiene (a leak of the outbound key cannot forge
  entitlement writes) at the cost of more config.
- The Starter/Pro numbers of this migration — i.e. *when* step 3 happens — is
  an operational call, not a code one. The spec only requires that step 3 be
  possible without a coordinated restart.

## Resolved open questions

> Decided 2026-08-08, mirrored from the billing spec
> `2026-08-08-01-sign-service-requests-to-solidping.md`, which owns these
> decisions. These are directives — implement exactly as stated.

**Q: "Naming. `X-SP-*` matches nothing else in this codebase; Standard Webhooks
uses `webhook-id` / `webhook-timestamp` / `webhook-signature`. [...] Pick one and
use it in both repos."**

**Decision: `X-SP-*`.** The headers are exactly `X-SP-Signature: v1,<base64>`,
`X-SP-Timestamp`, `X-SP-Key-Id`, in both directions and in both repos. The
apparent code-sharing win from reusing the Standard Webhooks names is cosmetic:
that scheme signs `id.timestamp.body` while this one signs
`<timestamp>.<method>.<path>.<hex sha256 of raw body>`, so the canonical strings
differ regardless of header naming.

**Q: "Scope of the key. One key for both directions, or one per direction?"**

**Decision: one key set per direction.** Two independent ordered `{id, secret}`
sets (newest first; sign with the first, verify against any) — one for the
billing→OSS entitlements push, one for the OSS→billing `/api/v1/*` calls. A leak
of one direction's key must not be usable to forge the other. Billing holds the
mirror image, as `BILLING_SIGNING_KEYS_OUTBOUND` / `BILLING_SIGNING_KEYS_INBOUND`.

**Q: "Is `sub`/actor attribution wanted on the inbound push?"**

**Decision: no.** Entitlements writes stay attributed to "the billing service" as
a whole; there is exactly one outbound key id and it is not varied per trigger.
Trigger provenance (`webhook` / `reconcile` / `manual`) already lives in billing's
`push_log.trigger`.

**Q: "The Starter/Pro numbers of this migration — i.e. *when* step 3 happens."**

**Not a blocker** — the spec already states this is an operational call. The code
requirement stands: step 3 must be possible without a coordinated restart.

## Related

- `~/code/fclairamb/solidping-billing/specs/todos/2026-08-08-01-sign-service-requests-to-solidping.md`
  — the counterpart half (signing the outbound push, verifying inbound
  signatures, and splitting a conflated secret). This spec is **not**
  implementable end-to-end from this repository alone, and that spec owns the
  agreed migration order across the two repos.
- The `#bt=` upgrade token, implemented in billing spec `2026-07-11-03`, is the
  precedent this generalizes.

### Ordering constraint

Sequencing lives in the billing spec, but the constraint that matters here:
**this repo must verify signatures before billing starts sending them**, and
must keep accepting the legacy bearer until billing has stopped sending it.
Landing this spec's step 3 (flip legacy off) early breaks every entitlement
push.

### One finding that belongs to the other side

`BILLING_INBOUND_SECRET` in the billing service is used both as a static bearer
credential *and* as the HS256 key that verifies the `#bt=` tokens this repo
mints. So a leak of that bearer lets an attacker forge an upgrade token for any
org. The fix is billing-side (and is step 1 of the migration), but it is worth
knowing here, because the secret this repo stores as
`entitlements.billing_inbound_secret` is the same value — it is a signing key,
and it should stop being usable as a bearer.

## Implementation Plan

Scope: the **solidping (OSS) side only**. The billing repo implements the mirror
image under its own spec; nothing here touches it.

### 1. `server/internal/servicesig` — the scheme, in one package

New package owning the wire contract so both the verifier (inbound) and the
signer (outbound) share one definition and can never drift:

- `Key{ID, Secret}` and `KeySet []Key` (ordered, newest first), parsed from a
  JSON array with `ParseKeySet`. Empty/blank input → empty set (feature off).
- `Canonical(ts int64, method, path string, body []byte) string` →
  `<timestamp>.<METHOD>.<path>.<hex sha256 of raw body>`. This is the wire
  contract; a test pins the exact bytes for a known vector.
- `Sign(key, ts, method, path, body)` → base64(HMAC-SHA256) and
  `SignRequest(req, keys, body)` which sets `X-SP-Signature: v1,<b64>`,
  `X-SP-Timestamp`, `X-SP-Key-Id` using the **first** key.
- `Verify(keys, method, path, body, headers, now)` rejecting in order:
  absent/unknown key id → skew over 300s → signature mismatch
  (`hmac.Equal`, never `==`). Typed errors so the caller can log the reason
  while returning one generic 401.
- `LoadKeySet(ctx, reader, paramKey)` reading an ordered key set out of a
  system parameter via a tiny `SystemParamReader` interface (no import cycle).

### 2. System parameters (one key set per direction)

Following the existing `entitlements.*` convention:

| Key | Role | Billing mirror |
|---|---|---|
| `entitlements.service_signing_keys` | verify the billing→OSS entitlements push | `BILLING_SIGNING_KEYS_OUTBOUND` |
| `entitlements.outbound_signing_keys` | sign OSS→billing `/api/v1/*` calls | `BILLING_SIGNING_KEYS_INBOUND` |
| `entitlements.allow_legacy_service_token` | accept the legacy static bearer (**default `true`**) | — |

Seeded in `app/saas.go` from `SP_ENTITLEMENTS_SERVICE_SIGNING_KEYS`,
`SP_ENTITLEMENTS_OUTBOUND_SIGNING_KEYS`,
`SP_ENTITLEMENTS_ALLOW_LEGACY_SERVICE_TOKEN`, registered in
`internal/envcheck` so they do not warn as unknown, and wired into
`make dev-saas`.

### 3. Inbound verification — `ServiceSignature` middleware

Added to `internal/middleware/auth.go`, placed **before** `ServiceTokenBypass`
on the entitlements group:

- No `X-SP-Signature` header → pass through untouched (legacy bearer or normal
  user auth still applies).
- Header present → buffer the raw body (hash it, restore it for the handler),
  verify, and on success set the same `serviceAuthContextKey` so the downstream
  `RequireAuth`/`RequireOrgAccess` no-op exactly as they do today (cross-org
  writes preserved).
- Header present but invalid → one generic 401 immediately; the reason goes to
  the log, not the response.

`isServiceAuthorized` gains an exported `IsServiceAuthorized` so the handler
can honor it.

### 4. Legacy bearer, gated but ON by default

`ServiceTokenBypass` takes the allow-legacy parameter key. When the legacy
bearer matches **and** legacy is allowed → bypass as today, plus a deprecation
warning naming the path. When legacy is disallowed → no bypass, so the request
falls through and 401s.

The entitlements handler's own duplicate bearer check
(`Handler.authorize`) is made consistent: it trusts
`middleware.IsServiceAuthorized` first, and only falls back to its own bearer
comparison while legacy is allowed. Otherwise the flag would be bypassable
through the handler.

**Ordering constraint honored:** the default stays `true`. Flipping it off is
step 3 of the cross-repo migration and is a parameter flip, not a deploy —
explicitly out of scope for this change.

### 5. Outbound signing helper

`servicesig.Signer` (key set loaded from `entitlements.outbound_signing_keys`)
exposing `Sign(ctx, req, body)`, so the future checkout/portal proxy client is
signed from its first commit. The proxy client itself is **not** built here,
per the spec.

### 6. Tests

- `servicesig` unit tests: canonical-string vector pinned byte for byte,
  sign/verify round trip, tampered body, stale/future timestamp, unknown key
  id, rotation (two keys, either accepted), malformed headers, empty key set.
- Middleware/route-level tests over the real chain
  (`ServiceSignature` → `ServiceTokenBypass` → `RequireAuth` →
  `RequireOrgAccess` → handler) covering every acceptance criterion, including
  the cross-org write and both states of `allow_legacy_service_token`.
- Positive controls everywhere a negative is asserted: each 401 test shares its
  setup with a 200 test that differs only in the attacked field.

### 7. Docs

`wiki/features/entitlements.md`, `wiki/api-specification/entitlements.md` and
root `CLAUDE.md` describe the signed scheme, the two key sets, and the
rotation/migration procedure.
