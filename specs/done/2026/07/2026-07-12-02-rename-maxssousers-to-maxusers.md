# Rename entitlement limit maxSsoUsers → maxUsers (provider-neutral seat cap)

## Problem

The pricing decision of 2026-07-12 (solidping-marketing
`memory/decisions.md`) sells the seat lever as **"total users"** — a plain
seat cap on all org members, deliberately not an SSO feature gate (the
"sso.tax" backlash risk is the reason the lever was renamed).

The OSS model doesn't match:

- The wire field and storage key are `maxSsoUsers`
  (`server/internal/db/models/entitlements_payload.go`), and the
  entitlements PUT decoder (`DisallowUnknownFields`) rejects any other key.
  solidping-billing already tried to send `maxUsers` once (its commit
  ca4dfff, 2026-07-11) and had to be reverted because every push would
  have 400'd.
- Enforcement counts only SSO-linked members: `CheckSSOMembership`
  (`server/internal/entitlements/service.go`) counts distinct org members
  with a `user_providers` row, and is called from the 9 SSO auth services
  only. Members who joined by invitation/email don't count against the
  cap, so the marketed "total users" limit is not actually enforced.

## Change

1. **Wire/model**: rename the field to `MaxUsers` (JSON `maxUsers`) in
   `EntitlementLimits`, the entitlements core `Limits`, and the PUT
   decoder. Keep `maxSsoUsers` as a **deprecated decode-only alias**
   (maps onto the same field; sending both is a 400) so already-deployed
   billing services keep working during the transition.
2. **Semantics**: the cap counts **all organization members**. Rename
   `CheckSSOMembership` → `CheckMembership`, count org members (not
   `user_providers` joins), and enforce it at *every* membership-creation
   path: SSO first-login join **and** invitation acceptance
   (`user-registration-invitations` flow).
3. **Display**: usage page label becomes "Users".
4. **Defaults**: self-hosted default stays 30 seats (now meaning total
   members); SaaS default stays 5 (mirrors billing Free).
5. **Follow-up (billing repo)**: after this deploys, switch
   solidping-billing's `internal/entitlements` JSON tag back to
   `maxUsers` and update its `TestLimitsWireKeys` allowlist.

## Notes

- Existing `org_entitlements` payload rows carry `maxSsoUsers`; the
  unmarshal path must read it as the alias forever (v1 rows are not
  rewritten).
- The quota error `LimitName` string is user-visible in API errors —
  rename to `MaxUsers` and keep the old string accepted in any client
  that matches on it (grep first).

## Implementation Plan

1. **Model wire rename + alias** (`server/internal/db/models/entitlements_payload.go`):
   rename `EntitlementLimits.MaxSSOUsers` (`maxSsoUsers`) → `MaxUsers` (`maxUsers`).
   Add a custom `UnmarshalJSON` on `EntitlementLimits` that accepts `maxSsoUsers`
   as a decode-only alias mapping onto `MaxUsers`, rejects sending both keys
   (400 at the handler), and still rejects unknown limit keys (preserves the
   loud-typo contract). Marshal emits only `maxUsers`. Alias decode works forever
   for stored v1 rows via the same method.
2. **Core service semantics** (`entitlements/service.go`, `usage.go`, `defaults.go`):
   rename `CheckSSOMembership` → `CheckMembership`, count ALL org members via the
   renamed DB method, `QuotaError.LimitName` = `MaxUsers`. Update `merge`, defaults
   constants, comments.
3. **DB count-all** (`db/service.go`, `db/sqlite/sqlite.go`, `db/postgres/postgres.go`):
   rename `CountSSOMembersForOrg` → `CountMembersForOrg`, drop the `user_providers`
   JOIN so every org member counts.
4. **Enforcement wiring** (`handlers/auth/*`): rename interface method
   `CheckSSOMembership` → `CheckMembership` and wrapper `CheckSSOSlot` →
   `CheckMembershipSlot`; update all SSO call sites; add the cap check to
   `AcceptInvite` (invitation acceptance) and map `ErrEntitlementExceeded` → 403
   in `handleInvitationError`.
5. **API surface**: `handlers/entitlements/handler.go` mergePartial field;
   `openapi.yaml` `maxSsoUsers` → `maxUsers`; regenerate `pkg/client/client_generated.go`;
   CLI flag `--max-sso-users` → `--max-users` (keep old as alias).
6. **Display** (dash0): `hooks.ts` `maxSsoUsers` → `maxUsers`; `organization.usage.tsx`
   reads `maxUsers`; locale label "SSO users" → "Users" (key `ssoUsers` → `users`).
7. **Tests**: update entitlements/auth tests to the new names; add coverage for the
   `maxSsoUsers` alias decode (stored-JSON + PUT), both-keys-400, and that a
   provider-less member counts toward the cap.

(Item 5 "Follow-up (billing repo)" is out of scope — separate repo.)
