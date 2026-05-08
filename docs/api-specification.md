# API Specification

All API routes are prefixed with `/api/v1` unless otherwise noted. Organization-scoped routes use `:org` to refer to the organization slug.

## Conventions

- **Pagination**: Cursor-based. Use `cursor` and `limit` query parameters. Responses include `hasMore` and `cursor` for the next page. Endpoints that previously used `?size=` still accept it as a deprecated alias.
- **Filtering**: Multi-value filters use comma-separated values in singular form (e.g., `?checkUid=a,b`).
- **Search**: Use `q` for free-text search.
- **Optional includes**: Use `with` to request related data (e.g., `?with=last_result,check`).
- **JSON conventions**: camelCase for all JSON properties and query parameters.
- **List responses**: Always wrapped in `{ "data": [...] }`, never bare arrays.
- **Updates**: Use `PATCH` for partial updates.
- **Errors**: See [Error Responses](#error-responses) at the bottom.

---

## Management

### GET /api/mgmt/health
Health check. Auth: public

### POST /api/mgmt/report
Submit an in-app bug report (multipart/form-data). Public endpoint, optional bearer token for user attribution. Body fields: `url` (required), `comment`, `org`, `annotations`, `context` (JSON), `screenshot` (file). Returns `{ uid }`. The screenshot is stored as a `File` (group `reports`) and a GitHub issue is created asynchronously when `app.github.*` is configured.

### GET /api/v1/features
Return the active feature flags for the frontend (e.g. `{ "bugReport": true }`). Auth required.

### GET /api/mgmt/version
Returns server version, build hash, and build date. Auth: public

---

## Authentication (Public)

### POST /api/v1/auth/login
Email/password login. Returns access token, refresh token, and user info. Body accepts optional `org` field.

### POST /api/v1/auth/refresh
Refresh an expired access token using a refresh token.

### POST /api/v1/auth/register
Register a new user account. Sends a confirmation email.

### POST /api/v1/auth/confirm-registration
Confirm a registration via email token. Returns access token.

### POST /api/v1/auth/request-password-reset
Request a password reset email.

### POST /api/v1/auth/reset-password
Reset password using a reset token.

### GET /api/v1/auth/invite/:token
Get invitation details by token (used to pre-fill the accept-invite form).

### POST /api/v1/auth/accept-invite
Accept an organization invitation. Creates the user if needed and returns access token.

### POST /api/v1/auth/2fa/verify
Verify a 2FA code during login (when login returns a 2FA challenge).

### POST /api/v1/auth/2fa/recovery
Use a recovery code to bypass 2FA during login.

### GET /api/v1/auth/providers
List enabled authentication providers (password, OAuth providers). Auth: public

---

## Authentication (Authenticated)

### POST /api/v1/auth/logout
Logout and invalidate the current session. Auth: required

### POST /api/v1/auth/switch-org
Switch the user's active organization context. Auth: required

### GET /api/v1/auth/me
Get the current authenticated user's profile. Auth: required

### PATCH /api/v1/auth/me
Update the current user's profile (name, password, etc.). Auth: required

### GET /api/v1/auth/tokens
List all personal access tokens for the current user across all organizations. Auth: required

### DELETE /api/v1/auth/tokens/:tokenUid
Revoke a personal access token. Auth: required

### POST /api/v1/auth/2fa/setup
Begin 2FA setup. Returns a TOTP secret and QR code URI. Auth: required

### POST /api/v1/auth/2fa/confirm
Confirm 2FA setup by verifying a TOTP code. Auth: required

### DELETE /api/v1/auth/2fa
Disable 2FA for the current user. Auth: required

---

## OAuth Providers (Conditional)

Each provider is only registered if its `ClientID` is configured. All are public.

### GET /api/v1/auth/slack/login
### GET /api/v1/auth/slack/callback

### GET /api/v1/auth/google/login
### GET /api/v1/auth/google/callback

### GET /api/v1/auth/github/login
### GET /api/v1/auth/github/callback

### GET /api/v1/auth/microsoft/login
### GET /api/v1/auth/microsoft/callback

### GET /api/v1/auth/gitlab/login
### GET /api/v1/auth/gitlab/callback

### GET /api/v1/auth/discord/login
### GET /api/v1/auth/discord/callback

---

## Organizations

### POST /api/v1/orgs
Create a new organization. Auth: required

### GET /api/v1/orgs/:org/settings
Get organization settings. Auth: required

### PATCH /api/v1/orgs/:org/settings
Update organization settings. Auth: required (admin)

---

## Organization Tokens

### GET /api/v1/orgs/:org/tokens
List the current user's personal access tokens for this organization. Auth: required

### POST /api/v1/orgs/:org/tokens
Create a personal access token scoped to this organization. Auth: required

---

## Organization Invitations

### GET /api/v1/orgs/:org/invitations
List pending invitations. Auth: required (admin)

### POST /api/v1/orgs/:org/invitations
Create a new invitation (sends email). Auth: required (admin)

### DELETE /api/v1/orgs/:org/invitations/:uid
Revoke a pending invitation. Auth: required (admin)

---

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

---

## Entitlements

Per-org limits + boolean features. Owned by an external billing service
in SaaS, by org admins in self-hosted. The OSS knows nothing about plan
SKUs — only raw numbers and booleans. NULL on a limit means "unlimited";
NULL on a feature means "use the in-code default".

### GET /api/v1/orgs/:org/entitlements
Returns the resolved entitlements (defaults merged with the stored row),
plus live `usage` counts and a `stale` flag. Auth: any authenticated
org member.

### PUT /api/v1/orgs/:org/entitlements
Replaces the entitlement row. Body: `{limits, features, allowedCheckTypes,
displayName, externalRef, expiresAt, lastSyncedAt, metadata}`. Optional
`X-Entitlements-Reason` header is recorded on the audit log. Auth: a
valid `entitlements.service_token` (preferred for SaaS) OR an org admin
JWT when `entitlements.admin_writes_enabled` is true (default in
self-hosted). Returns the resolved entitlements.

### PATCH /api/v1/orgs/:org/entitlements
Same auth as PUT, but only updates the fields present in the body. Useful
for incremental changes (e.g. extend a trial). Returns the resolved
entitlements.

### GET /api/v1/orgs/:org/entitlements/audits
Returns the entitlement audit rows for the org, newest first. Optional
`?limit=` query parameter (default 50, max 200). Auth: org admin or
service token.

System parameters:
- `entitlements.service_token` — secret bearer token for the billing
  service. Unset in self-hosted by default.
- `entitlements.admin_writes_enabled` — boolean, default true in
  self-hosted, set to false in SaaS to lock writes to the service token.
- `entitlements.upgrade_url_template` — template URL with `{org}` placed
  for the slug; surfaced on GET as `upgradeUrl` so the frontend can
  render an upgrade affordance. Empty in self-hosted.
- `entitlements.stale_after_days` — days before a billing-service row is
  considered stale and the resolver falls back to defaults. Default 0
  (no stale fallback) in self-hosted.

---

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

---

## Checks

### GET /api/v1/orgs/:org/checks
List monitoring checks. Auth: required

Query parameters:
- `with` - comma-separated: `last_result`, `last_status_change`
- `labels` - filter by labels, format: `key1:value1,key2:value2`
- `checkGroupUid` - filter by check group UID
- `q` - free-text search
- `internal` - filter by internal flag
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100)

### POST /api/v1/orgs/:org/checks
Create a new check. Type can be inferred from the config URL. Name and slug are auto-generated if omitted. Auth: required

### GET /api/v1/orgs/:org/labels
Autocomplete suggestions for label keys (or values for a given key) used by checks in the org. Returns rows sorted by usage count DESC, then `value` ASC for stable ties. Auth: required.

Query parameters:
- `key` - if omitted, lists distinct keys; if provided, lists distinct values for that key
- `q` - case-insensitive prefix filter on the returned `value`
- `limit` - page size (default 50, silently clamped to max 200)

Response:
```json
{
  "data": [
    {"value": "environment", "count": 12},
    {"value": "team", "count": 8}
  ]
}
```

`count` is the number of distinct checks carrying that key (or key/value pair). Empty result returns `{"data": []}` (200), not 404.

### POST /api/v1/orgs/:org/checks/validate
Validate a check configuration without persisting. Auth: required

Request body accepts the same shape as `POST /checks` plus optional
`dependsOn` (slug-keyed) and `slug` (so cycle / self-edge / duplicate /
cross-org / unknown-parent validators can run before the check exists).
Returns `{"valid": true}` or `{"valid": false, "fields": [...]}` with one
field-level entry per failing validator.

### GET /api/v1/orgs/:org/checks/export
Export all checks as JSON. Auth: required

Each `ExportCheck` carries an optional `dependsOn` array of
`{parentSlug, kind, description?}` entries, sorted by `parentSlug` for
deterministic diffs. The field is `omitempty` — exports for orgs with no
dep edges stay byte-identical to the pre-dependsOn shape.

### POST /api/v1/orgs/:org/checks/import
Import checks from JSON. Auth: required

Two-pass when any entry carries `dependsOn`: pass 1 upserts every check
unchanged, pass 2 resolves `parentSlug` → check UID against the now-current
org state and applies an additive merge of edges (new edges created;
existing edges with same kind+description are no-ops; differing edges are
updated). Cycle / self-edge / unknown-parent failures are reported per row
in the existing `errors` array. Pass 2 is skipped silently for any check
whose pass-1 upsert failed, with an explicit
`skipped dependsOn: pass-1 upsert failed for this check` error.

### GET /api/v1/orgs/:org/checks/:checkUid
Get a single check by UID or slug. Auth: required

Query parameters:
- `with` - comma-separated optional includes (e.g., `last_result`)

### PUT /api/v1/orgs/:org/checks/:slug
Upsert a check by slug (create if not exists, update if exists). Auth: required

Request body optionally carries `dependsOn`, pointer-typed (`*[]…`) so the
handler can distinguish three states:
- **absent** (`null` / field missing) → existing dep edges untouched. This
  is the back-compat default for tooling that doesn't know about deps —
  partial PUT must not nuke deps.
- **explicit empty array** (`[]`) → all dep edges for this check are
  deleted.
- **non-empty array** → set the dep edges to exactly this list (destructive
  sync). All cycle / self-edge / cross-org / kind / duplicate validators
  run before any write; any failure aborts the whole operation. Caveat: the
  dep apply currently runs after the check upsert outside any wrapping
  transaction — a failed dep apply leaves the check itself updated. A
  follow-up will move the whole flow into a single transaction.

### PATCH /api/v1/orgs/:org/checks/:checkUid
Update a check. Auth: required

### DELETE /api/v1/orgs/:org/checks/:checkUid
Delete a check (soft delete). Auth: required

### GET /api/v1/orgs/:org/checks/:checkUid/events
List events for a specific check. Auth: required

Query parameters:
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

---

## Check Connections

Manage notification/integration connections attached to individual checks.

### GET /api/v1/orgs/:org/checks/:check/connections
List all connections for a check. Auth: required

### PUT /api/v1/orgs/:org/checks/:check/connections
Set (replace) all connections for a check. Auth: required

### POST /api/v1/orgs/:org/checks/:check/connections/:connection
Add a connection to a check. Auth: required

### DELETE /api/v1/orgs/:org/checks/:check/connections/:connection
Remove a connection from a check. Auth: required

### GET /api/v1/orgs/:org/checks/:check/connections/:connection
Get connection-specific settings for a check. Auth: required

### PATCH /api/v1/orgs/:org/checks/:check/connections/:connection
Update connection-specific settings for a check. Auth: required

---

## Check Types

### GET /api/v1/check-types
List all check types with metadata and server-level activation status. Auth: public

### GET /api/v1/check-types/samples
List sample configurations for all check types. Supports `?type=` filter. Auth: public

### GET /api/v1/orgs/:org/check-types
List check types resolved for the organization (merges server and org settings). Auth: required

---

## Check Groups

### GET /api/v1/orgs/:org/check-groups
List check groups. Auth: required

### POST /api/v1/orgs/:org/check-groups
Create a check group. Auth: required

### GET /api/v1/orgs/:org/check-groups/:uid
Get a check group. Auth: required

### PATCH /api/v1/orgs/:org/check-groups/:uid
Update a check group. Auth: required

### DELETE /api/v1/orgs/:org/check-groups/:uid
Delete a check group. Auth: required

---

## Results

### GET /api/v1/orgs/:org/results
List monitoring results across checks. Auth: required

Query parameters:
- `checkUid` - comma-separated check UIDs or slugs
- `checkType` - comma-separated check types
- `status` - comma-separated: `up`, `down`, `unknown`
- `region` - comma-separated regions
- `periodType` - comma-separated period types
- `periodStartAfter` - RFC3339 timestamp
- `periodEndBefore` - RFC3339 timestamp
- `with` - comma-separated optional fields
- `cursor` - pagination cursor
- `limit` - page size (default 100, max 1000). Also accepts `?size=` as a deprecated alias.

---

## Incidents

### GET /api/v1/orgs/:org/incidents
List incidents. Auth: required

Query parameters:
- `checkUid` - comma-separated check UIDs
- `state` - comma-separated states (e.g., `open`, `resolved`)
- `since` - RFC3339 timestamp
- `until` - RFC3339 timestamp
- `with` - comma-separated: `check`
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

### GET /api/v1/orgs/:org/incidents/:uid
Get a single incident. Auth: required

Query parameters:
- `with` - comma-separated: `check`

### GET /api/v1/orgs/:org/incidents/:uid/events
List events for a specific incident. Auth: required

Query parameters:
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

---

## Events

### GET /api/v1/orgs/:org/events
List events across the organization. Auth: required

Query parameters:
- `eventType` - comma-separated event types
- `checkUid` - filter by check UID
- `incidentUid` - filter by incident UID
- `cursor` - pagination cursor
- `limit` - page size (default 20, max 100). Also accepts `?size=` as a deprecated alias.

---

## Regions

### GET /api/v1/regions
List all available global regions. Auth: public

### GET /api/v1/orgs/:org/regions
List regions relevant to the organization. Auth: required

---

## Channels (formerly: Connections)

Manage notification channels (Slack, Discord, email, webhook, etc.) at the
organization level.

> **Naming alignment in progress.** The dashboard, internal docs, and
> these endpoints' canonical name is **channel**. The legacy name
> **connection** stays accepted as a path alias for one release so
> external clients (CLIs, MCP tools) can switch over without breakage.
> The `/connections` paths return identical responses to the
> `/channels` paths and will be removed in a future revision per
> [`specs/done/2026/05/2026-05-07-03-align-channel-and-connection-naming.md`](#).

### GET /api/v1/orgs/:org/channels (alias: /api/v1/orgs/:org/connections)
List all channels. Auth: required

### POST /api/v1/orgs/:org/channels (alias: /api/v1/orgs/:org/connections)
Create a new channel. Auth: required

### GET /api/v1/orgs/:org/channels/:uid (alias: /api/v1/orgs/:org/connections/:uid)
Get a channel. Auth: required

### PATCH /api/v1/orgs/:org/channels/:uid (alias: /api/v1/orgs/:org/connections/:uid)
Update a channel. Auth: required

### DELETE /api/v1/orgs/:org/channels/:uid (alias: /api/v1/orgs/:org/connections/:uid)
Delete a channel. Auth: required

---

## Status Pages

### GET /api/v1/orgs/:org/status-pages
List status pages. Auth: required

### POST /api/v1/orgs/:org/status-pages
Create a status page. Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid
Get a status page. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid
Update a status page. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid
Delete a status page. Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections
List sections of a status page. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections
Create a section. Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Get a section. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Update a section. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Delete a section. Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources
List resources in a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources
Add a resource to a section. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid
Update a resource. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid
Remove a resource. Auth: required

### Public Status Page Views

### GET /api/v1/status-pages/:org
View the default status page for an organization. Auth: public

### GET /api/v1/status-pages/:org/:slug
View a specific status page by slug. Auth: public

---

## Maintenance Windows

### GET /api/v1/orgs/:org/maintenance-windows
List maintenance windows. Auth: required

### POST /api/v1/orgs/:org/maintenance-windows
Create a maintenance window. Auth: required

### GET /api/v1/orgs/:org/maintenance-windows/:uid
Get a maintenance window. Auth: required

### PATCH /api/v1/orgs/:org/maintenance-windows/:uid
Update a maintenance window. Auth: required

### DELETE /api/v1/orgs/:org/maintenance-windows/:uid
Delete a maintenance window. Auth: required

### GET /api/v1/orgs/:org/maintenance-windows/:uid/checks
List checks associated with a maintenance window. Auth: required

### PUT /api/v1/orgs/:org/maintenance-windows/:uid/checks
Set (replace) the checks associated with a maintenance window. Auth: required

---

## Badges

### GET /api/v1/orgs/:org/checks/:check/badges/:format
Get a status badge for a check (e.g., SVG). Auth: public

---

## Files

Generic file storage. Bytes live behind a pluggable backend (local FS or S3); metadata lives in the `files` table. Authenticated read/list/delete are scoped to the requesting organization. Public access is via signed URL only.

### GET /api/v1/orgs/:org/files
List files for an organization. Query: `q`, `limit`, `offset`. Auth: required.

### GET /api/v1/orgs/:org/files/:uid
Get file metadata. Auth: required.

### GET /api/v1/orgs/:org/files/:uid/content
Stream file bytes (org-scoped). Auth: required.

### DELETE /api/v1/orgs/:org/files/:uid
Soft-delete a file (the blob in storage is left in place). Auth: required.

### GET /pub/files/:uid?exp=&sig=
Public read via HMAC-signed URL. `exp` (unix seconds) and `sig` are required. Returns 403 on bad signature, 410 on expired, 404 on unknown / soft-deleted file. Auth: public (signature gates access).

---

## Heartbeat

Token-based authentication via the URL identifier. Used for cron job and heartbeat monitoring.

### POST /api/v1/heartbeat/:org/:identifier
Send a heartbeat ping. Auth: public (token in URL)

### GET /api/v1/heartbeat/:org/:identifier
Send a heartbeat ping (GET variant for simple HTTP clients). Auth: public (token in URL)

---

## Workers API

Used by distributed check workers. Authentication is via worker registration token.

### POST /api/v1/workers/register
Register a new worker. Auth: worker token

### POST /api/v1/workers/heartbeat
Send a worker heartbeat. Auth: worker token

### POST /api/v1/workers/claim-jobs
Claim pending check jobs for execution. Auth: worker token

### POST /api/v1/workers/submit-result
Submit a check execution result. Auth: worker token

---

## Jobs

Job management for background tasks. Routes are registered without authentication middleware at the router level (auth may be checked in handlers).

### GET /api/v1/orgs/:org/jobs
List jobs. Auth: required

### POST /api/v1/orgs/:org/jobs
Create a job. Auth: required

### GET /api/v1/orgs/:org/jobs/:uid
Get a job. Auth: required

### DELETE /api/v1/orgs/:org/jobs/:uid
Cancel a job. Auth: required

---

## Slack Integration

Inbound endpoints for Slack app integration.

### GET /api/v1/integrations/slack/oauth
Slack OAuth callback handler. Auth: public (Slack flow)

### POST /api/v1/integrations/slack/events
Slack Events API webhook. Auth: Slack signature verification

### POST /api/v1/integrations/slack/command
Slack slash command handler. Auth: Slack signature verification

### POST /api/v1/integrations/slack/interaction
Slack interactive component handler. Auth: Slack signature verification

---

## MCP (Model Context Protocol)

### POST /api/v1/mcp
MCP endpoint for AI tool integrations. Auth: required (PAT token, org derived from token)

---

## System (Super Admin)

### GET /api/v1/system/parameters
List all system parameters. Auth: super-admin

### GET /api/v1/system/parameters/:key
Get a system parameter by key. Auth: super-admin

### PUT /api/v1/system/parameters/:key
Set a system parameter. Auth: super-admin

### DELETE /api/v1/system/parameters/:key
Delete a system parameter. Auth: super-admin

### POST /api/v1/system/test-email
Send a test email to verify email configuration. Auth: super-admin

---

## Test Endpoints (Development Only)

These endpoints are always available:

### POST /api/v1/test/jobs
Create a test email job. Auth: public (dev only)

### GET /api/v1/fake
Fake API endpoint for testing. Auth: public (dev only)

These endpoints are only available when `SP_RUNMODE=test`:

### GET /api/v1/test/state-entries
List internal state entries. Auth: public (test mode only)

### POST /api/v1/test/checks/bulk
Bulk-create checks for testing. Auth: public (test mode only)

### DELETE /api/v1/test/checks/bulk
Bulk-delete checks for testing. Auth: public (test mode only)

### POST /api/v1/test/generate-data
Generate synthetic monitoring data. Auth: public (test mode only)

### DELETE /api/v1/test/checks/all
Delete all checks. Auth: public (test mode only)

---

## Other Endpoints

### GET /openapi.yaml
OpenAPI schema definition. Auth: public

### GET /docs
Swagger/OpenAPI documentation UI. Auth: public

### GET /metrics
Prometheus metrics (only when `prometheus.enabled` is set). Auth: public

---

## Error Responses

All errors return JSON with:
```json
{
  "title": "Human-readable description",
  "code": "MACHINE_READABLE_CODE",
  "detail": "Detailed explanation"
}
```

Standard error codes:
- `INTERNAL_ERROR` - Unexpected server error
- `VALIDATION_ERROR` - Input validation failed
- `NOT_FOUND` - Resource not found
- `UNAUTHORIZED` - Authentication required
- `FORBIDDEN` - Permission denied
- `CONFLICT` - Resource conflict (duplicate, etc.)
- `ORGANIZATION_NOT_FOUND` - Organization does not exist
- `USER_NOT_FOUND` - User does not exist
- `CHECK_NOT_FOUND` - Check does not exist
