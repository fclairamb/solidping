# Organizations

Organizations, their settings, org-scoped tokens, invitations, members, and the
self-service membership-request flow.

## Organizations

### POST /api/v1/orgs
Create a new organization. Auth: required

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
Create a new invitation (sends email). Auth: required (admin)

### DELETE /api/v1/orgs/:org/invitations/:uid
Revoke a pending invitation. Auth: required (admin)

## Members

### GET /api/v1/orgs/:org/members
List organization members. Auth: required

### POST /api/v1/orgs/:org/members
Add a member to the organization. Auth: required (admin)

### GET /api/v1/orgs/:org/members/:uid
Get a member's details. Auth: required

### PATCH /api/v1/orgs/:org/members/:uid
Update a member's role. Auth: required (admin)

### DELETE /api/v1/orgs/:org/members/:uid
Remove a member from the organization. Auth: required (admin)

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
`user`). Sends a decision email to the requester.

### POST /api/v1/orgs/:org/membership-requests/:uid/reject
Reject a request. Auth: required (admin). Body:
`{"reason":"<optional>"}`. Sends a decision email to the requester. The
requester can re-submit only after the cooldown
(`membership_requests.cooldown_days`, default 7).
