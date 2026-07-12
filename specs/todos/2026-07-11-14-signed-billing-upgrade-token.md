# Mint a signed billing upgrade token for org admins

> Mirror of solidping-billing
> `specs/todos/2026-07-11-03-admin-auth-for-upgrade-downgrade.md` — read
> that spec first; it holds the threat model and the rejected
> alternatives. (This file was first written 2026-07-11 and lost to a
> batch working-tree reset; recreated 2026-07-12.)

## Problem

The billing service's `/api/public/*` endpoints are unauthenticated and
keyed only by org slug. Since billing gained in-place plan changes and
period-end cancellation, an anonymous caller who knows an org slug can
change a victim org's paid plan (billed to the victim's card on file),
schedule a downgrade-to-Free, or read its plan state. Org slugs are not
secrets. Billing cannot validate OSS sessions itself (HS256 symmetric
session key; org-admin role lives in the OSS DB, not in token claims).

## Change (OSS side)

1. New system parameter `entitlements.billing_inbound_secret`, set to
   the same value as billing's `BILLING_INBOUND_SECRET`.
2. In `GET /api/v1/orgs/:org/entitlements`
   (`server/internal/handlers/entitlements/handler.go`, which already
   computes `upgradeUrl` from `entitlements.upgrade_url_template`):
   - If the caller is an **org admin**, mint an HS256 JWT signed with
     that secret, claims
     `{purpose: "billing", org: <slug>, sub: <user uid>, email, iat, exp: iat+1h}`,
     and append it to the upgrade URL as a fragment: `#bt=<token>`.
     Fragments are never sent to servers, so it can't leak via Referer
     or logs.
   - If the caller is **not** an org admin, omit `upgradeUrl` entirely —
     the dashboard already renders the Upgrade button only when
     `upgradeUrl` is present, so no frontend change is needed.
3. Verification is billing-side and stateless (signature, expiry,
   purpose, org match) — no OSS callback.
