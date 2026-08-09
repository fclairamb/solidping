---
model: sonnet
effort: high
---

# Any org member — including a viewer — can add, re-role and remove members

## Problem

The member-management write routes are registered through the plain `orgGroup(...)`
helper in [server/internal/app/server.go:1086](server/internal/app/server.go:1086),
which applies only `RequireAuth` + `RequireOrgAccess`
([server.go:553](server/internal/app/server.go:553)):

```go
orgMembers := orgGroup("/orgs/:org/members")
orgMembers.GET("", membersHandler.ListMembers)
orgMembers.POST("", membersHandler.AddMember)          // no admin gate
orgMembers.GET("/:uid", membersHandler.GetMember)
orgMembers.PATCH("/:uid", membersHandler.UpdateMember) // no admin gate
orgMembers.DELETE("/:uid", membersHandler.RemoveMember)// no admin gate
```

There is no admin check in the handler either
([server/internal/handlers/members/handler.go](server/internal/handlers/members/handler.go)):
`AddMember` / `UpdateMember` / `RemoveMember` validate the payload and delegate
straight to `members.Service`. So **any** member of an organization — a `user`,
or even a read-only `viewer` — can currently add members, change other members'
roles (including promoting themselves to `admin`), and remove members.

Compare `/orgs/:org/checks` writes, which go through a group that adds
`authMiddleware.RequireOrgAdmin`
([server.go:772](server/internal/app/server.go:772)) — that is the pattern this
route family should have followed.

Scope of what is *already* covered, so it isn't re-implemented:

- The owner role (spec `2026-08-08-11`) closes the worst case: only an owner may
  grant `owner`, or modify/remove an owner — enforced inside `members.Service`
  against the **live** membership row, not the JWT role claim. Everything below
  owner is still completely unguarded.
- `RequireOrgAdmin` is already hierarchical
  ([server/internal/middleware/auth.go:453](server/internal/middleware/auth.go:453)
  → `requireOrgRole(models.MemberRoleAdmin, …)` using `MemberRole.AtLeast`), so
  an `owner` passes the admin gate and super admins are let through. No
  middleware work is needed — only wiring.

The dashboard compounds it: the members page
([web/dash0/src/routes/orgs/$org/organization.members.tsx](web/dash0/src/routes/orgs/$org/organization.members.tsx))
gates only on `isOwner` (locking owner rows). It never consults
`user.isAdmin`, which already exists in `AuthContext`
([web/dash0/src/contexts/AuthContext.tsx:31](web/dash0/src/contexts/AuthContext.tsx:31)
— "at least admin", owner and superadmin included). A `viewer` therefore sees
live role dropdowns and delete buttons, and today they *work*.

The API docs already describe the intended behaviour
([wiki/api-specification/orgs.md:86-103](wiki/api-specification/orgs.md:86)):
POST/PATCH/DELETE are documented as "Auth: required (admin; **owner** to …)".
The code simply never enforced the admin half.

## Proposal

### 1. Gate the write routes behind `RequireOrgAdmin`

In [server/internal/app/server.go:1086](server/internal/app/server.go:1086), split
the group the way `orgChecks` / `orgChecksAdmin` do
([server.go:759-776](server/internal/app/server.go:759)):

```go
// Reads stay open to any member of the org.
orgMembers := orgGroup("/orgs/:org/members")
orgMembers.GET("", membersHandler.ListMembers)
orgMembers.GET("/:uid", membersHandler.GetMember)

// Writes are admin-only. `members.Service` additionally requires *owner* to
// touch an owner or to grant ownership (spec 2026-08-08-11); this gate is the
// floor beneath that, not a replacement for it.
orgMembersAdmin := api.NewGroup("/orgs/:org/members").
    Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
orgMembersAdmin.POST("", membersHandler.AddMember)
orgMembersAdmin.PATCH("/:uid", membersHandler.UpdateMember)
orgMembersAdmin.DELETE("/:uid", membersHandler.RemoveMember)
```

Keep the comment explaining *why* reads stay open (the escalation-policy editor
and the member picker read this list as a plain user — see below).

### 2. Tests

Table-driven, in the members package (next to
[server/internal/handlers/members/owner_test.go](server/internal/handlers/members/owner_test.go),
which is the closest precedent for role-matrix coverage), plus a routing-level
test that actually exercises the middleware chain — a handler-only test would
pass even with the gate missing, so the test must go through the registered
router.

Matrix over caller role × verb:

| caller role | POST / PATCH / DELETE | GET |
|---|---|---|
| `viewer` | 403 `FORBIDDEN` | 200 |
| `user` | 403 `FORBIDDEN` | 200 |
| `admin` | success | 200 |
| `owner` | success | 200 |

Assertions:

- The 403 body is the standard error shape —
  `{"title": …, "code": "FORBIDDEN", "detail": …}` — not a bare status.
- **Negative control**: a `viewer` PATCH must leave the target member's role
  unchanged in the database. A test that only asserts the status code would pass
  against a gate that 403s *after* mutating.
- **Positive control**: the same PATCH as an `admin` must succeed and change the
  row — proving the 403 comes from the role, not from a malformed request.
- An `owner` must still pass the admin gate (guards against a future
  equality-instead-of-hierarchy regression in `requireOrgRole`).
- The owner-only rules from `2026-08-08-11` must still hold on top: an `admin`
  granting `owner`, or modifying/removing an owner, is still refused by
  `members.Service`.

### 3. Check what depended on the gap

Known callers, to be re-verified as part of the change:

- [web/dash0/e2e/escalation-policies.spec.ts:126](web/dash0/e2e/escalation-policies.spec.ts:126)
  — `GET /orgs/test/members` to resolve a userUid. Read, unaffected.
- [web/dash0/e2e/org-owner-delete.spec.ts:152](web/dash0/e2e/org-owner-delete.spec.ts:152)
  — `POST /orgs/:org/members` as the **owner** token. Unaffected.

Sweep `web/dash0/e2e/` and `web/dash0/src/` once more for any other write call,
and confirm no non-admin path (test seeding, an onboarding flow, the invitation
or membership-request accept path) relies on a `user`/`viewer` being able to
write members. If one does, it must move to a proper server-side path rather
than have the gate relaxed.

### 4. Dashboard follow-through

The gate alone would leave a `viewer` clicking controls that now 403. In
[organization.members.tsx](web/dash0/src/routes/orgs/$org/organization.members.tsx),
add an `isAdmin` gate alongside the existing `isOwner` one (`user?.isAdmin ?? false`):
render the role `Select` and the remove button as disabled/read-only for
non-admins, mirroring how `isLocked` already handles owner rows. Follow
`CLAUDE.md`'s frontend rules — check the design reference first, keep delete red
with `Trash2`, keep it usable on mobile.

Add a dash0 E2E assertion that a non-admin sees the members list read-only, with
a server-side proof first (the raw `PATCH` is 403 even bypassing the UI), in the
style of the existing check at
[org-owner-delete.spec.ts:172](web/dash0/e2e/org-owner-delete.spec.ts:172).

### 5. Docs

[wiki/api-specification/orgs.md:86-103](wiki/api-specification/orgs.md:86)
already documents these as admin-only, so no wording change is required — but
re-read it after the change and correct it if the enforced behaviour ends up
differing in any detail. Check whether
[server/internal/app/openapi/openapi.yaml:2855](server/internal/app/openapi/openapi.yaml:2855)
describes the auth requirement for these operations and update it if so.

## Open questions

- Should `POST /orgs/:org/members` (adding an existing user directly) be
  admin-only or owner-only? Admin-only matches the documented behaviour and the
  invitation flow, so that is the assumption here.
- The sibling groups `orgInvitations`, `orgSettings` and `orgMembershipRequests`
  ([server.go:574-589](server/internal/app/server.go:574)) are commented
  "admin-only checked in handler". That claim is out of scope for this spec, but
  worth a quick verification pass while in the area — if any of them is likewise
  unenforced, file it as its own spec rather than widening this one.

## Resolved open questions

Both questions above were already settled by the spec itself; restated here as
directives so there is nothing left to interpret.

> **Should `POST /orgs/:org/members` be admin-only or owner-only?**

**Decision: admin-only**, exactly as the §1 code block shows. It matches the
documented behaviour in `wiki/api-specification/orgs.md` and the invitation
flow. The owner-only rules from spec `2026-08-08-11` still apply on top, inside
`members.Service` — this gate is the floor beneath them, not a replacement.

> **The sibling groups `orgInvitations`, `orgSettings` and
> `orgMembershipRequests` are commented "admin-only checked in handler".**

**Decision: out of scope — verify, report, do not fix here.** Spend a few
minutes confirming whether each of those handlers really does check for admin.
If one does not, **file a new spec** in `specs/todos/` describing the gap and
mention it in your final report. Do **not** widen this change to cover them.
