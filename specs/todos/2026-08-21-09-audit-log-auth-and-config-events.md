---
model: opus
effort: high
---

# The audit trail only covers checks and incidents — add auth, membership, and config events

## Problem

The `events` table is check/incident-centric: every `EventType` in
[event.go](server/internal/db/models/event.go) is `check.*` or `incident.*`.
There is no record of who logged in (password, SSO, passkey — or failed to),
who invited or removed a member, who changed a role, who created or revoked an
API token or agent key, who edited an integration or escalation policy, or who
ran a config-as-code apply. ISO/SOC2-minded buyers ask for exactly this list,
and today the honest answer is "only for entitlements" — the entitlements
audit trail ([entitlements/service.go:165](server/internal/entitlements/service.go:165),
`NewOrgEntitlementAudit`) exists separately and proves the pattern works.

## Proposal

Extend the existing org-scoped events pipeline rather than building a second
store — one queryable trail, one retention story, one UI.

### Schema

- Add nullable actor metadata to `events` (+ migration): `actor_user_uid`,
  `actor_type` (`user` / `api_token` / `service` / `system`), `source_ip`,
  `user_agent`. Existing check/incident events may leave them null.
- IP capture is a config knob (`audit.capture_ip`, default on) so
  GDPR-sensitive self-hosters can turn it off.

### New event-type families

- `auth.login_succeeded` / `auth.login_failed` / `auth.logout` (method:
  password / oidc / saml / ldap / oauth-provider / passkey)
- `auth.token_created` / `auth.token_revoked` (API tokens, agent keys)
- `member.invited` / `member.joined` / `member.removed` / `member.role_changed`
- `integration.created` / `integration.updated` / `integration.deleted`
- `escalation_policy.*`, `oncall_schedule.*`, `status_page.*`,
  `maintenance_window.*` (created/updated/deleted each)
- `config.applied` (config-as-code apply — with a summary count of
  created/updated/deleted resources, not the payload)
- `org.settings_updated`

Emission happens at the service layer of the respective handler packages
(`handlers/auth`, `members`, `integrations`, `escalationpolicies`,
`oncallschedules`, `statuspages`, `maintenancewindows`, `checks` apply) — not
in HTTP middleware, so events carry domain meaning and internal callers are
covered too.

### Redaction & flood control

- **Never** store secrets, password material, token values, or full config
  payloads. Update events record changed *field names* (and safe old→new
  values for non-sensitive fields only); token events store the token's name
  and prefix, never the value.
- `auth.login_failed` is a brute-force amplification vector: fold repeats
  (same email + IP) within a short window into one event with a counter, and
  rate-limit total failed-login events per org per hour so the table cannot
  be flooded.

### Surface

- Existing events endpoints gain `type` (family prefix match) and
  `actorUserUid` filters; auth events are visible to org admins/owners only.
- dash0: an org-level **Audit** page (admin-gated) — filterable table (time,
  actor, type, target, IP), following the design-reference table patterns,
  mobile-usable.
- Retention: a config knob (default 365 days) enforced by a cleanup job —
  align with however existing events retention works rather than inventing a
  second mechanism; if none exists, add it for both.
- Document the event catalogue in `wiki/api-specification/` and the docs
  site's security page.

### Out of scope

Streaming export (SIEM webhook / syslog) — worth a future spec once the
in-product trail exists; note it in the roadmap, don't build it here.

---

## Implementation Plan

### D0 — `actor_user_uid` is the existing `actor_uid`

`events` already ships `actor_uid uuid references users(uid)` and
`actor_type varchar(20) check (actor_type in ('system','user'))`. Adding a
second `actor_user_uid` column beside a column that already *is* the actor's
user reference would be a duplicate with a split-brain failure mode. So:

- the **column** stays `actor_uid` (FK to `users`), documented as the
  spec's `actor_user_uid`;
- the **API query parameter** is named `actorUserUid` exactly as the spec
  asks (with `actorUid` accepted as an alias);
- `actor_type` gets its check constraint **widened** to the spec's four
  values: `system` / `user` / `api_token` / `service`.

Genuinely new columns: `source_ip`, `user_agent`.

### 1. Schema (migration `015_v0_18_0`, section `audit-actor-metadata`, both dialects)

- `events.source_ip` (varchar(45) / text), `events.user_agent` (text).
- Widen the `actor_type` check to `('system','user','api_token','service')`.
- `idx_events_org_type_created (organization_uid, event_type, created_at desc)`
  — the org-scoped `type` filter has no covering index today.
- `idx_events_created (created_at)` — the retention sweep has none either.
- `.down.sql` unwinds in reverse (drop indexes, restore the 2-value check,
  drop the columns).
- Tests: section ships in both dialects (banner present exactly once),
  columns/constraint present on a freshly migrated DB, the widened constraint
  actually *accepts* `api_token` and still *rejects* garbage (negative control),
  and the down section executes against a populated migrated DB.

### 2. Model (`db/models/event.go`)

- `ActorTypeAPIToken = "api_token"`, `ActorTypeService = "service"`.
- `Event.SourceIP *string`, `Event.UserAgent *string`.
- `ListEventsFilter`: `EventTypePrefixes []string` (family prefix match),
  `ExcludeEventTypePrefixes []string` (the non-admin `auth.*` mask),
  `ActorUID *string`.
- ~26 new `EventType` constants across the families the spec lists.

### 3. `server/internal/audit` — the emitter

- `Actor{Type, UserUID, SourceIP, UserAgent}` + `WithActor`/`ActorFromContext`
  so IP/UA reach the service layer without rewriting ~40 signatures. Populated
  by a thin request middleware (**capture** in middleware, **emission** in the
  service layer — the spec's rule).
- `Record(ctx, dbSvc, orgUID, eventType, payload)` — pulls the actor from
  context, applies redaction, writes one `events` row. Best-effort: an audit
  write never fails the business operation, it logs.
- `SetCaptureIP(bool)` fed by `audit.capture_ip` (default on). Off ⇒
  `source_ip` is nil; `user_agent` is unaffected.
- **Redaction**: `Redact(payload)` drops any key whose name matches the
  sensitive denylist (password/secret/token/key/credential/authorization/
  webhook_url/dsn/private/hash/salt/...) at any nesting depth, and caps
  value length. `Changes(before, after)` returns changed *field names* plus
  safe old→new values for non-sensitive scalar fields only.
- Target identity travels in the payload (`target_type`, `target_uid`,
  `target_name`) — no schema change.

### 4. Flood control (`audit/loginfailed.go`)

- `FailedLoginFolder`: fold repeats of (org, email, IP) inside a window
  (default 10 min) into ONE event carrying `count` / `first_at` / `last_at`,
  via a new `UpdateEventPayload(ctx, uid, payload)` on `db.Service` (both
  dialects). Append-only stays true for every other event type.
- Per-org-per-hour ceiling on *newly created* `auth.login_failed` rows
  (default 60). Over the ceiling the observation is dropped, with a single
  WARN per bucket.
- Both are in-memory, mutex-guarded, injectable clock; entries expire.

### 5. Emission call sites (service layer)

| family | where |
|---|---|
| `auth.login_succeeded` | `auth.completeLogin` — the single funnel for password / passkey / oauth / ldap; `method` in the payload |
| `auth.login_failed` | `auth.Login` (bad password + LDAP reject), through the folder |
| `auth.logout` | `auth.Logout` |
| `auth.token_created` / `auth.token_revoked` | `auth.CreatePAT` / `auth.RevokeToken` (`token_kind: pat`), `agents.MintEnrollmentToken` / `DeleteEnrollmentToken` / `RevokeAgent` (`token_kind: agent_key`) — name + prefix only, never the value |
| `member.invited` | `auth.CreateInvitation` |
| `member.joined` | `auth.AcceptInvite`, `members.AddMember` |
| `member.removed` | `members.RemoveMember` |
| `member.role_changed` | `members.UpdateMember` (only when the role actually moved) |
| `integration.*` | `integrations.CreateIntegration` / `UpdateIntegration` / `DeleteIntegration` |
| `escalation_policy.*` | `escalationpolicies.CreatePolicy` / `UpdatePolicy` / `DeletePolicy` |
| `oncall_schedule.*` | `oncallschedules.CreateSchedule` / `UpdateSchedule` / `DeleteSchedule` |
| `status_page.*` | `statuspages.CreateStatusPage` / `UpdateStatusPage` / `DeleteStatusPage` |
| `maintenance_window.*` | `maintenancewindows.Create/Update/DeleteMaintenanceWindow` |
| `config.applied` | `checks.ApplyChecks` — created/updated/deleted/unmanaged counts + manifest name, never the payload |
| `org.settings_updated` | `auth.UpdateOrgSettings` — changed field names |

### 6. API surface

- `GET /orgs/:org/events` gains `type` (comma-separated family prefixes,
  e.g. `?type=auth,member`) and `actorUserUid` (alias `actorUid`).
- **Auth events are admin/owner-only**: the handler resolves the caller's org
  role; a non-admin always gets `ExcludeEventTypePrefixes = ["auth."]`, so
  both an unfiltered listing and an explicit `?type=auth` return nothing
  rather than leaking. Enforced server-side, not by hiding UI.
- Response gains `sourceIp`, `userAgent`, and resolved `actorName` /
  `actorEmail` for the page's actor UIDs.
- OpenAPI updated + `go generate ./pkg/client/...` re-run.

### 7. Retention

- `db.DeleteEventsBefore(ctx, before, limit)` (both dialects).
- New `events_cleanup` job (daily, self-rescheduling, batched) modelled on
  `job_jobs_cleanup.go`; registered in `jobdef/types.go`, the jobtypes
  registry, and `job_startup.go`.
- Retention resolved through the established precedence
  (`SP_AUDIT_RETENTION_DAYS` env → `audit.retention_days` DB parameter →
  koanf → default **365**).

### 8. Config

`AuditConfig{ CaptureIP bool (default true); RetentionDays int (default 365) }`
under `audit`. Both keys are multi-word/koanf-hostile in the usual way, so
they get a manual `applyAuditEnv` reader plus `manualReaderEnvVars` entries
(`SP_AUDIT_CAPTURE_IP`, `SP_AUDIT_RETENTION_DAYS`).

### 9. dash0

- Every new event type gets a tone + label in **all four** locales, and is
  either in `EVENT_TYPE_REGISTRY` (pinned in `BINDING_PAIRS`) or in
  `INTENTIONALLY_UNMAPPED` with a reason.
- New admin-gated `/orgs/$org/organization/audit` page (the `organization`
  layout is already admin-gated) — filterable table: time, actor, event,
  target, IP. URL-driven filters, design-reference primitives, mobile-usable.
- Tab added to the organization layout.
- Playwright E2E.

### 10. Docs

- `wiki/api-specification/` — the event catalogue + the new filters.
- Docs site: a security/audit-log feature page.
