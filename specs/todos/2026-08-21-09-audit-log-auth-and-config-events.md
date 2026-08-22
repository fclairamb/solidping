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
