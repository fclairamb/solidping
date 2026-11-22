---
model: opus
effort: medium
---

# A same-role member PATCH silently turns an OIDC-group admin grant into a permanent local grant

## Problem
Found by the security scan of 2026-11-22 (finding AU1). Severity: Medium. Who can trigger it: an org admin whose admin role came from the OIDC org-admin group claim (`auth.oidc.org_admin_groups`, spec 2026-11-21-02), acting on their own membership. No help from another admin is needed.

The IdP group claim is meant to be the deprovisioning control for org admins. An admin role granted by the claim is tagged `organization_members.role_source = 'oidc_group'`. `revokeOrgAdmin` demotes it at the next login once the user is out of every org-admin group, and only touches a claim grant (`server/internal/handlers/auth/oidc_groups.go:369-372`).

The flaw:
- `UpdateMember` (`server/internal/handlers/members/service.go:384-395`) builds `OrganizationMemberUpdate{Role: &newRole}` without `RoleSource`. It writes even when `newRole` equals the current role.
- `authorizeRoleChange` (`members/service.go:437-468`) lets admin→admin through. Nothing stops a caller from changing their own membership.
- `UpdateOrganizationMember` sets `role_source` to the update's `RoleSource` (NULL here) whenever `Role` is set, even if the value does not change. See `server/internal/db/postgres/postgres.go:1218-1221` and `server/internal/db/sqlite/sqlite.go:1204`. The contract test `server/internal/db/dbcontract/identity.go:240-247` asserts this for a real role change.
- The audit event is recorded only when the role moved (`members/service.go:413`), so the write leaves no trace.

The attack:
1. While still in the group, the admin PATCHes their own membership with the role they already have.
2. `role_source` becomes NULL, which counts as a local grant.
3. Once they leave the IdP group, `revokeOrgAdmin` returns early forever. They keep admin, and the trail shows nothing.

In OIDC-only mode this is the simple way to keep the role. With local login off, a second local account to fall back on is not an option. The super-admin half is not affected: `setSuperAdminSource` only writes `super_admin_source` when the update names it or clears it (`server/internal/db/postgres/postgres.go:870-880`).

## Proposal
1. `members/service.go` `UpdateMember`: when `newRole == member.Role`, do not write the role. Return the member as is, so a same-role PATCH is a true no-op that keeps `role_source`.
2. `db/postgres/postgres.go:1218` and `db/sqlite/sqlite.go:1204`: overwrite `role_source` only when the role value actually changes. For example, add `WHERE role <> ?` to the role half, or compare in the service first. Keep "a real role change naming no source is a local grant": an admin's hand-made role change is still allowed to take over a claim grant.
3. A role change that clears an `oidc_group` source (a real change by another admin) must record `member.role_changed` with the previous source in its payload. That way an operator can see a claim grant turned into a local one.

## Tests
- `server/internal/handlers/members/service_test.go`: a member with `role=admin`, `role_source='oidc_group'` gets PATCHed with `role: admin` and keeps `role_source='oidc_group'`. Fails before the fix: the source becomes NULL. Then `revokeOrgAdmin` (or `syncGroupRoles` with no matching group) demotes them to `user`.
- Positive control, same file: an owner or another admin PATCHes that member to `user`, then back to `admin`. `role_source` is now NULL, and the next group-less login leaves the role alone (a real local grant still wins).
- `server/internal/db/dbcontract/identity.go`: a same-role `UpdateOrganizationMember` keeps `role_source`. The existing real-change case still clears it.
- Audit: the real change that clears `oidc_group` records one `member.role_changed` event carrying the previous source.

## Resolved open questions
- Do not refuse admins from changing their own role: the no-op fix (a same-role PATCH writes nothing and keeps `role_source`) is enough and keeps the ownership-transfer flow. (autopilot: recommended answer)
