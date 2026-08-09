# Organizations

Organizations, their settings, org-scoped tokens, invitations, members, and the
self-service membership-request flow.

## Member roles

Four roles, ordered **owner > admin > user > viewer**. Gates are hierarchical:
an owner passes every admin gate, an admin passes every user gate, and so on.
Never compare a role for equality when you mean "at least" — use
`models.MemberRole.AtLeast` on the backend, or `claims.HasOrgRole` when the role
comes from a JWT claim.

| Role | What it adds |
|---|---|
| `owner` | Everything an admin can do, **plus** deleting the organization and granting/revoking ownership |
| `admin` | Full read/write on the org's resources, member management (except owners) |
| `user` | Read/write on monitoring resources |
| `viewer` | Read-only |

Ownership rules:

- The creator of an organization is its owner — via `POST /api/v1/orgs`, and via
  every connector (Slack, Discord, Google, GitHub, GitLab, Microsoft, OIDC,
  SAML, LDAP), whose org-creation paths funnel through the shared login
  admission policy's zero-member bootstrap.
- Only an owner may grant `owner`, or change/remove a member who is an owner.
  An admin attempting either gets `403 FORBIDDEN`.
- An organization can never be left ownerless: demoting or removing its last
  owner is a `409 CONFLICT`. Multiple owners are allowed, which is also the
  ownership-transfer path — promote a second owner, then demote yourself.
- `owner` is **not** an invitable role, and not a role a membership request can
  be approved with: those grants are consumed later, when the granter's role is
  no longer verifiable.

## Organizations

### POST /api/v1/orgs
Create a new organization. Auth: required (any authenticated user).
The caller becomes the org's **owner**, and the response carries a session
scoped to the new org. The org is always created for the caller — the owner
comes from the access token, never from the body. A slug freed by a deleted
organization can be claimed again.

### DELETE /api/v1/orgs/:org
Delete an organization. Auth: required (**owner**) — an admin gets
`403 FORBIDDEN`. Body: `{"slug":"<org-slug>"}`, retyped as confirmation;
a mismatch is a `422 VALIDATION_ERROR`. Returns `204`.

Deletion stops every check immediately (the org's scheduler rows are removed),
soft-deletes its checks and memberships, and revokes every org-scoped token —
including the caller's own, so the dashboard drops to the org switcher. From
that instant the slug **404s everywhere**: dashboard API, public status pages,
badges and the embed widget. The slug is released for reuse and no alias,
tombstone or redirect is left behind. There is no restore endpoint; recovery is
a manual database intervention.

### GET /api/v1/orgs/:org/settings
Get organization settings. Auth: required

### PATCH /api/v1/orgs/:org/settings
Update organization settings. Auth: required (admin)

## Organization Tokens

### GET /api/v1/orgs/:org/tokens
List the current user's personal access tokens for this organization. Auth: required

### POST /api/v1/orgs/:org/tokens
Create a personal access token scoped to this organization. Auth: required

## Organization Invitations

### GET /api/v1/orgs/:org/invitations
List pending invitations. Auth: required (admin)

### POST /api/v1/orgs/:org/invitations
Create a new invitation (sends email). Auth: required (admin).
Role must be `admin`, `user` or `viewer` — `owner` is not invitable.

### DELETE /api/v1/orgs/:org/invitations/:uid
Revoke a pending invitation. Auth: required (admin)

## Members

### GET /api/v1/orgs/:org/members
List organization members. Auth: required

### POST /api/v1/orgs/:org/members
Add a member to the organization. Auth: required (admin; **owner** to add
another owner)

### GET /api/v1/orgs/:org/members/:uid
Get a member's details. Auth: required

### PATCH /api/v1/orgs/:org/members/:uid
Update a member's role. Auth: required (admin; **owner** to grant `owner` or to
change an owner's role). Demoting the last owner is a `409 CONFLICT`.

### DELETE /api/v1/orgs/:org/members/:uid
Remove a member from the organization. Auth: required (admin; **owner** to
remove an owner). Removing the last owner is a `409 CONFLICT`.

## Membership Requests

A confirmed user with no membership in an org can ask to join by slug.
Org admins can approve or reject. Each (org, user) pair has at most one
row; subsequent requests update it in place per the state machine
(pending → approved | rejected | cancelled, with re-request after
cooldown for rejected and immediate re-request for cancelled).

### POST /api/v1/auth/membership-requests
Open or re-open a request. Auth: required.
Body: `{"orgSlug":"<slug>","message":"<optional>"}`.
Errors: `ORGANIZATION_NOT_FOUND`, `ALREADY_A_MEMBER`,
`REQUEST_PENDING`, `REQUEST_COOLDOWN_ACTIVE`.

### GET /api/v1/auth/membership-requests
List the caller's own request history. Auth: required.

### DELETE /api/v1/auth/membership-requests/:uid
Cancel the caller's own request. Auth: required (owner). Admins must
use the reject endpoint instead.

### GET /api/v1/orgs/:org/membership-requests
List incoming requests for the org. Auth: required (admin).
Query parameters: `status` (optional, e.g. `pending`).

### POST /api/v1/orgs/:org/membership-requests/:uid/approve
Approve a request and create the membership in one transaction.
Auth: required (admin). Body: `{"role":"user|admin|viewer"}` (default
`user`; `owner` cannot be granted this way). Sends a decision email to the requester.

### POST /api/v1/orgs/:org/membership-requests/:uid/reject
Reject a request. Auth: required (admin). Body:
`{"reason":"<optional>"}`. Sends a decision email to the requester. The
requester can re-submit only after the cooldown
(`membership_requests.cooldown_days`, default 7).
