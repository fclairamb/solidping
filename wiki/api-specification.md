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
Export all checks as JSON. Auth: **admin** (org admin role required)

Each `ExportCheck` carries an optional `dependsOn` array of
`{parentSlug, kind, description?}` entries, sorted by `parentSlug` for
deterministic diffs. The field is `omitempty` — exports for orgs with no
dep edges stay byte-identical to the pre-dependsOn shape.

> **Back-compat note (2026-06-20):** export/import were previously gated by
> authentication only (any org member). They are now **admin-only**, alongside
> the new apply endpoint, because they read/mutate the whole check set. Scripts
> that called these as a non-admin user must switch to an admin token.

### POST /api/v1/orgs/:org/checks/import
Import checks from JSON. Auth: **admin** (org admin role required)

Two-pass when any entry carries `dependsOn`: pass 1 upserts every check
unchanged, pass 2 resolves `parentSlug` → check UID against the now-current
org state and applies an additive merge of edges (new edges created;
existing edges with same kind+description are no-ops; differing edges are
updated). Cycle / self-edge / unknown-parent failures are reported per row
in the existing `errors` array. Pass 2 is skipped silently for any check
whose pass-1 upsert failed, with an explicit
`skipped dependsOn: pass-1 upsert failed for this check` error.

### POST /api/v1/orgs/:org/checks/apply
Reconcile checks against a declarative manifest (config-as-code). Auth:
**admin** (org admin role required). This is the *reconcile sibling* of
`/import` — idempotent upsert-by-slug plus delete-by-absence within a bounded,
opted-in managed scope.

**Request body.** The existing export document shape (`{version, organization,
checks[]}`), accepted as **JSON or YAML** (sniffed from `Content-Type` and the
first non-space byte). YAML is the hand-authoring surface; JSON is what export
emits — both parse to the same plan.

**Managed scope.** Apply stamps every check it owns with a reserved label
`solidping.io/managed=<manifest-name>`, where the manifest name is the document's
`organization` field (falling back to the org slug). The reconcile scope is
exactly the checks carrying that label. Hand-created checks (no managed label)
are reported as `unmanaged` and are **never** adopted, modified destructively,
or deleted.

**Plan / reconcile semantics.** Matching is on `slug` within the managed scope:
- `create` — slug in the manifest, absent from the org.
- `update` — managed slug present in both.
- `unmanaged` — slug exists **without** the managed label (reported only).
- `delete` — managed check absent from the manifest (delete-by-absence).
- `rename` — a manifest check with `previousSlug` (or `uid`) referencing an
  existing managed check reconciles the rename in place instead of delete+create.

**Secret references.** Config string values may contain `${env:NAME}` and
`${param:KEY}` references, resolved **server-side at apply time** (env vars; the
`parameters` table — org-scoped first, then system-wide) into the existing
encrypted `config_private` envelope. The committed manifest stays secret-free.
A missing/unresolvable reference is a hard apply error. When
`SP_ENCRYPTION_MASTER_KEY` is unset (plaintext fallback), resolving a secret ref
emits a `warnings[]` entry rather than refusing — the resolved value lands in
plaintext config.

**Deletion safety (belt-and-suspenders).** Delete-by-absence happens **only**
when all of: (a) `?prune=true` is set, (b) the check carries the managed label,
and (c) the delete count is within the deletion cap (default 10). Beyond the
cap, apply refuses with `409 CONFLICT` unless `?force=true`.

Query parameters:
- `dryRun=true` — compute and return the plan only; mutate nothing.
- `prune=true` — enable delete-by-absence for managed, absent checks.
- `force=true` — lift the deletion cap for this apply.
- `deletionCap=<n>` — override the default cap (0 ⇒ default 10).

**Response** (extended import result):
```json
{
  "manifest": "default",
  "dryRun": false,
  "pruned": true,
  "created": 1, "updated": 2, "deleted": 1, "unmanaged": 0,
  "plan": [{"slug": "api", "action": "update"}, {"slug": "old", "action": "delete"}],
  "warnings": [],
  "errors": []
}
```

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

## Check notify channels

Manage the notify-capable integrations ("channels") attached to individual
checks. Canonical path is `/integrations`; `/channels` is the alias for the
notify role; `/connections` is the legacy path removed at PR-E. All three
return identical responses while present.

### GET /api/v1/orgs/:org/checks/:check/integrations (alias: /channels; removed: /connections)
List all notify channels for a check. Auth: required

### PUT /api/v1/orgs/:org/checks/:check/integrations (alias: /channels; removed: /connections)
Set (replace) all notify channels for a check. Auth: required

### POST /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Add a notify channel to a check. Auth: required

### DELETE /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Remove a notify channel from a check. Auth: required

### GET /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Get channel-specific settings for a check. Auth: required

### PATCH /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Update channel-specific settings for a check. Auth: required

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

### GET /api/v1/orgs/:org/events/stream
Live update hint stream (Server-Sent Events). Auth: required (org membership
enforced — non-members get 403).

Holds the response open as `text/event-stream` and pushes org-scoped, data-free
hints when resources change. Events:
- `hello` — first event, `{"protocol":1}`
- `hint` — `{"kinds":["results","checks","incidents","events","jobs"]}`;
  clients invalidate the matching caches and refetch over the normal REST API
- `resync` — the server may have missed changes (LISTEN/NOTIFY reconnect);
  refetch everything once
- `: ping` comment lines every ~25s keep the connection alive

Notes:
- Delivery is best-effort by design; the client keeps a lazy fallback poll.
- High-volume kinds (`results`, `jobs`) are coalesced to ≤1 hint/org/sec per
  API instance; status/incident transitions are immediate.
- The server closes the stream at access-token expiry; reconnect with a fresh
  token.
- `SP_REALTIME_ENABLED=false` → 404 (clients keep polling). Config knobs:
  `SP_REALTIME_FLUSH_INTERVAL` (1s), `SP_REALTIME_PING_INTERVAL` (25s),
  `SP_REALTIME_MAX_CONNECTIONS` (1000 per instance).
- Ops: the PostgreSQL LISTEN session requires a session-mode connection —
  PgBouncer transaction pooling is unsupported for the realtime listener.

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

> **Naming alignment.** The canonical name for these endpoints is now
> **integration** (the umbrella entity — Slack, webhook, email, Freebox).
> **/channels** is kept as a path alias for one release cycle (it is the
> prior name; "channel" survives only as the notify-capable *role*).
> **/connections** is the original legacy name and is **removed at PR-E**.
> All three paths return identical responses while present. See
> [`specs/done/2026/05/2026-05-29-01-channels-to-integrations-rename.md`](#).

### GET /api/v1/orgs/:org/integrations (alias: /api/v1/orgs/:org/channels; removed: /api/v1/orgs/:org/connections)
List all integrations. Auth: required

### POST /api/v1/orgs/:org/integrations (alias: /api/v1/orgs/:org/channels; removed: /api/v1/orgs/:org/connections)
Create a new integration. Auth: required

### GET /api/v1/orgs/:org/integrations/:uid (alias: /api/v1/orgs/:org/channels/:uid)
Get an integration. Auth: required

### PATCH /api/v1/orgs/:org/integrations/:uid (alias: /api/v1/orgs/:org/channels/:uid)
Update an integration. Auth: required

### DELETE /api/v1/orgs/:org/integrations/:uid (alias: /api/v1/orgs/:org/channels/:uid)
Delete an integration. Auth: required

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

## Escalation Policies

Manage escalation policies — ordered steps of notification targets that fire
when an incident is not acknowledged. The `:id` path param is resolved as
**uid-or-slug**: a value that parses as a UUID matches the policy `uid`,
otherwise it matches the `slug`. Prefer the `uid` as a stable identifier
(the `slug` is mutable via PATCH).

### GET /api/v1/orgs/:org/escalation-policies
List escalation policies (headers only, steps not expanded). Auth: required

### POST /api/v1/orgs/:org/escalation-policies
Create an escalation policy with its steps and targets. Auth: required

### GET /api/v1/orgs/:org/escalation-policies/:id
Get a single escalation policy (with expanded steps and targets) by **uid or
slug**. Returns `404 NOT_FOUND` for an unknown identifier. Auth: required

### PATCH /api/v1/orgs/:org/escalation-policies/:id
Update an escalation policy by **uid or slug**. When `steps` is present the
entire step list is replaced. Auth: required

### DELETE /api/v1/orgs/:org/escalation-policies/:id
Delete an escalation policy by **uid or slug** (soft delete). Returns
`409 ESCALATION_POLICY_IN_USE` when an open incident still references it.
Auth: required

---

## On-Call Schedules

Manage on-call rotation schedules, their rosters, overrides, and iCal feeds.
The `:id` path param is resolved as **uid-or-slug**: a value that parses as a
UUID matches the schedule `uid`, otherwise it matches the `slug`. Prefer the
`uid` as a stable identifier (the `slug` is mutable via PATCH).

### GET /api/v1/orgs/:org/on-call-schedules
List on-call schedules. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules
Create an on-call schedule with its initial roster. Auth: required

### GET /api/v1/orgs/:org/on-call-schedules/:id
Get a single schedule by **uid or slug**, including the current on-call user.
Returns `404 NOT_FOUND` for an unknown identifier. Auth: required

### PATCH /api/v1/orgs/:org/on-call-schedules/:id
Update a schedule by **uid or slug**. When `userUids` is present the roster
is rewritten. Auth: required

### DELETE /api/v1/orgs/:org/on-call-schedules/:id
Delete a schedule by **uid or slug** (soft delete). Auth: required

### GET /api/v1/orgs/:org/on-call-schedules/:id/preview
Preview the rotation over a window. Query: `from` (RFC3339, default now),
`days` (1–365, default 14). Auth: required

### GET /api/v1/orgs/:org/on-call-schedules/:id/overrides
List overrides on the schedule. Query: `from`, `until` (RFC3339). Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:id/overrides
Create an override. Auth: required

### DELETE /api/v1/orgs/:org/on-call-schedules/:id/overrides/:overrideUid
Delete an override. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:id/ical-feed/enable
Enable the public iCal feed and return its secret + URL. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:id/ical-feed/rotate
Rotate the iCal feed secret. Old URLs stop working. Auth: required

### POST /api/v1/orgs/:org/on-call-schedules/:id/ical-feed/disable
Disable the iCal feed. Subscribers begin receiving 410. Auth: required

### GET /api/v1/on-call-schedules/:secret/feed.ics
Public iCal feed. The secret in the URL authorizes access. Auth: public

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

## Network Discovery

On-demand host discovery. Found hosts land in `discovered_hosts` and can be listed, promoted to a check, or dismissed. Each host carries a `source` discriminator (`"lan"` for the CIDR scanner, `"freebox"` for Freebox LAN discovery). All routes are under `/api/v1/orgs/:org/discovery` and require auth + org access.

### POST /api/v1/orgs/:org/discovery/scans
Launch a CIDR network-discovery scan. Body `{ "cidrs": [...], "ports": [...] }`. Auth: admin. Large ranges are accepted: the scan is created as a `network_discovery_plan` job (its UID is the scan UID) that fans out into bounded `network_discovery` child jobs of ≤4096 addresses each, so hosts appear progressively. Returns `{ "data": <job> }`. `422 DISCOVERY_RANGE_TOO_LARGE` if the range exceeds the overall ceiling (`MaxScanChunks` = 256 chunks ≈ 1M addresses / a /12). `409 DISCOVERY_ALREADY_RUNNING` if a scan is already in flight for the org (a plan or any non-stale child pending/running; children whose `updated_at` is older than 30m are ignored).

### POST /api/v1/orgs/:org/discovery/freebox-scans
Launch a Freebox LAN-discovery run against a paired Freebox channel. Body `{ "channelUid": "..." }`. Auth: admin. Validates the channel is a paired Freebox channel before queueing. Returns `{ "data": <job> }` (same shape as `/scans`). Errors: `409 FREEBOX_NOT_GRANTED` (channel not paired), `404 NOT_FOUND` (no such Freebox channel), `409 DISCOVERY_ALREADY_RUNNING`.

### GET /api/v1/orgs/:org/discovery/scans
List discovery runs (`network_discovery_plan`, standalone `network_discovery`, and `freebox_lan_discovery`), newest first. Child `network_discovery` jobs (those carrying a `parentJobUid` in config) are filtered out — the plan job represents the scan. Auth: required. Returns `{ "data": [<job>, ...] }`.

### GET /api/v1/orgs/:org/discovery/scans/:jobUid
Get one discovery run. Auth: required. For a `network_discovery_plan` scan the response also carries a `progress` block: `{ "totalChunks", "completedChunks", "failedChunks", "runningChunks", "pendingChunks", "derivedStatus", "hostCount" }`. `derivedStatus` is `running` while the plan is pending/running or any child is pending/running, `success` once all children are terminal, `failed` only if the plan itself failed.

### POST /api/v1/orgs/:org/discovery/scans/:jobUid/cancel
Stop a running fan-out scan. Auth: admin. Cancels the plan job if still pending, then soft-deletes every pending child chunk; children already running finish naturally. Returns `204 No Content`. `404 NOT_FOUND` if no such scan exists for the org.

### GET /api/v1/orgs/:org/discovery/hosts
List discovered hosts for the org. Auth: required. Query params: `jobUid`, `promoted` (`true`/`false`), `source` (singular, comma-separated, e.g. `?source=lan,freebox`). Returns `{ "data": [<discoveredHost>, ...] }`. Each `discoveredHost` includes a `source` field.

### POST /api/v1/orgs/:org/discovery/hosts/:uid/promote
Promote a discovered host to a check. Body `{ "checkType": "...", ... }`. Auth: admin.

### DELETE /api/v1/orgs/:org/discovery/hosts/:uid
Dismiss (soft-delete) a discovered host. Auth: admin.

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
MCP endpoint for AI tool integrations. Auth: required — either a `mcp`/`mcp:read`-scoped
PAT pasted as a bearer token (back-compat) or an OAuth-issued access token (see OAuth below).
Org is derived from the token. On missing/invalid auth this endpoint returns **401** with a
`WWW-Authenticate: Bearer resource_metadata="<issuer>/.well-known/oauth-protected-resource"`
header so standard MCP clients can discover the authorization server and start the OAuth flow.
OAuth-issued tokens are audience-bound (RFC 8707) to the MCP resource and rejected here if their
`aud` does not include `<issuer>/api/v1/mcp`.

---

## OAuth 2.1 (MCP authorization server)

SolidPing is an embedded OAuth 2.1 authorization server for the MCP resource (spec
2026-06-20-03). Standard MCP clients (Claude Desktop, claude.ai remote connector, `mcp-remote`)
self-onboard via discovery → register → authorize+consent → token, with no hand-pasted token.
Issuer = the SolidPing base URL; the resource is `<issuer>/api/v1/mcp`. Access tokens reuse the
existing HS256 JWT format; refresh tokens rotate. PKCE (`S256`) is mandatory.

### GET /.well-known/oauth-protected-resource
RFC 9728 protected-resource metadata. Public, no auth. Advertises `resource` (the MCP URL),
`authorization_servers` (the issuer), `scopes_supported` (`mcp`, `mcp:read`), and
`bearer_methods_supported`.

### GET /.well-known/oauth-authorization-server
RFC 8414 authorization-server metadata. Public, no auth. Advertises `authorization_endpoint`,
`token_endpoint`, `registration_endpoint`, `jwks_uri`,
`code_challenge_methods_supported=["S256"]`, `grant_types_supported=["authorization_code",
"refresh_token"]`, `response_types_supported=["code"]`, and `scopes_supported=["mcp","mcp:read"]`.

### GET /.well-known/openid-configuration
Alias of the authorization-server metadata (many clients probe this path). Public, no auth.

### GET /.well-known/jwks.json
JWKS endpoint (`jwks_uri`). Public, no auth. The v1 signing key is symmetric (HS256), so the
secret is never published — this serves a well-formed but empty key set. The MCP resource server
validates tokens itself; clients do not verify locally. Asymmetric keys are a documented follow-on.

### POST /api/v1/oauth/register
RFC 7591 dynamic client registration. Public. Accepts `redirect_uris` (required), `client_name`,
`grant_types`, `response_types`, `scope`, `token_endpoint_auth_method`. Native clients are public
(PKCE + loopback redirects `http://127.0.0.1:*` / `http://localhost:*` / `http://[::1]:*`) and get
no secret; a client requesting a secret-based auth method is confidential and gets a `client_secret`
returned once. Redirect URIs must be https or http-loopback. Returns `client_id` (+ `client_secret`
for confidential clients).

### GET /api/v1/oauth/authorize
Authorization endpoint. Requires a logged-in dashboard session (the `access_token` cookie); if
absent, redirects to `/dash0/login?returnTo=…` and back. Validates `client_id`, `redirect_uri`
(exact match against the registered set, loopback ignores the port), `response_type=code`, PKCE
`code_challenge` + `code_challenge_method=S256` (both required; `plain` and missing are rejected),
`scope ⊆ {mcp, mcp:read}`, and `resource` (must equal the MCP resource). On success redirects to the
dashboard consent screen (`/dash0/orgs/:org/oauth/consent`).

### POST /api/v1/oauth/authorize
Consent decision. Re-validates the request, requires a session, and reads `decision` (`approve` /
`deny`). On approve, mints a single-use, short-TTL authorization code bound to
client→redirect→PKCE-challenge→resource→scope→user/org and redirects to the client's `redirect_uri`
with `code` + `state`. On deny, redirects with `error=access_denied`.

### POST /api/v1/oauth/token
Token endpoint (form-encoded). `grant_type=authorization_code` exchanges `code` + `code_verifier`
(+ matching `client_id`, `redirect_uri`) for an access token and a refresh token; the PKCE verifier
is checked against the stored S256 challenge and the code is consumed single-use.
`grant_type=refresh_token` rotates the refresh token (the presented one is revoked atomically and a
new pair issued). Access tokens are JWTs with `aud` = the MCP resource and the consented scopes,
short-lived; refresh tokens are revoked on logout and PAT revoke. Errors use the RFC 6749 §5.2 JSON
shape (`{ "error": "...", "error_description": "..." }`).

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
