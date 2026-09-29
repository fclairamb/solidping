# SaaS mode and entitlements wiring

Moved from the root agent file. Read when working on `SP_DEPLOYMENT_MODE=saas`, the billing service or signed entitlement pushes.

`SP_DEPLOYMENT_MODE=saas` switches per-org defaults to the SaaS tier and lets a
separate billing service (`../solidping-billing`) drive plan upgrades. Per-org
limits live in `org_entitlements` (`maxChecks`, `maxUsers` — `maxSsoUsers` is a
deprecated decode-only alias, `maxChecksPerMinute`, `maxSlos`) plus display-only plan identity (`displayName`,
`displayEmoji`, e.g. "🚀 Team") — both shown on the org **Usage** page
(`/orgs/$org/organization/usage`).

The billing service writes entitlements via `PUT /api/v1/orgs/:org/entitlements`.
It proves identity by **signing** the request — HMAC-SHA256 over
`<timestamp>.<METHOD>.<path>.<sha256 body>`, sent as `X-SP-Signature: v1,<b64>` /
`X-SP-Timestamp` / `X-SP-Key-Id` — verified by the `ServiceSignature` middleware
(`internal/middleware/auth.go`, scheme in `internal/servicesig`) ahead of the
normal `RequireAuth` chain, so cross-org writes stay possible. Keys live in two
independent ordered `{id, secret}` sets, one per direction:
`entitlements.service_signing_keys` (verify the inbound push) and
`entitlements.outbound_signing_keys` (sign our calls to billing). Rotation is
"add the new key to both sides, then drop the old" — no lockstep restart.

The original static bearer (`entitlements.service_token`, let through by
`ServiceTokenBypass`) is **legacy**: still accepted while
`entitlements.allow_legacy_service_token` is true (the default) and logged as
deprecated on every use, so retiring it is a parameter flip rather than a
coordinated deploy. See `wiki/features/entitlements.md` for the migration order.

The `#bt=` upgrade token appended to the dashboard's `upgradeUrl` is signed with
its **own** secret, `entitlements.billing_upgrade_token_secret` (env
`SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`, mirroring billing's
`BILLING_UPGRADE_TOKEN_SECRET`). `entitlements.billing_inbound_secret` is a
**bearer only** — leaking a credential that travels on every service call must
not also be the power to mint an upgrade token for any org, so collapsing the two
back into one value is a security regression. While the dedicated parameter is
unset the minter falls back to the bearer (WARN once per process); if both are
set to the *same* value, boot logs an ERROR and still starts. Operator migration
(both ends prefer-new / accept-old, so deploy order does not matter):

1. Deploy this — nothing moves, the fallback mints exactly as before.
2. Generate one new secret, set it on both sides.
3. Confirm billing's fallback warning has stopped.
4. Set `BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET=false` on billing.

**Step 4 is what closes the vulnerability** — steps 1–3 only make it closeable.

`make dev-saas` seeds the SaaS system parameters from
`SP_ENTITLEMENTS_SERVICE_TOKEN`, `SP_ENTITLEMENTS_SERVICE_SIGNING_KEYS`,
`SP_ENTITLEMENTS_OUTBOUND_SIGNING_KEYS`,
`SP_ENTITLEMENTS_ALLOW_LEGACY_SERVICE_TOKEN` and
`SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE` (the dashboard "Upgrade" link target);
run it alongside `../solidping-billing` `make dev` for the full upgrade loop.
See `server/internal/app/saas.go`.
