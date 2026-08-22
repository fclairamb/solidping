---
sidebar_position: 18
title: Audit log
---

# Audit log

SolidPing records who did what, when, and from where — sign-ins, membership
changes, API tokens, and every configuration change — in one org-scoped,
append-only trail. It is the answer to the questions an ISO 27001 or SOC 2
reviewer asks, and to the more urgent ones you ask at 3am ("who changed this
escalation policy?").

Find it in the dashboard under **Organization → Audit**. It is visible to org
**admins and owners** only.

## What is recorded

| Category | Events |
|---|---|
| Authentication | sign-in succeeded / failed, sign-out |
| Credentials | API token and agent enrollment key created / revoked |
| Membership | invited, joined, removed, role changed |
| Integrations | created, updated, deleted |
| Escalation policies | created, updated, deleted |
| On-call schedules | created, updated, deleted |
| Status pages | created, updated, deleted |
| Maintenance windows | created, updated, deleted |
| Config as code | apply, with created/updated/deleted counts |
| Organization | settings updated |
| Checks & incidents | the pre-existing check and incident lifecycle events |

Each entry carries the acting user (or "system" / an API token), the time, the
object acted on, and — unless you turn it off — the client IP address and user
agent.

Events are recorded by the service that performs the change, not by a request
log sitting in front of it. That means a change made through the API, the CLI,
the MCP server or a config-as-code apply is recorded identically, and each
entry says what actually changed rather than which URL was called.

## What is never recorded

The audit log is readable by every org admin, so it is designed on the
assumption that whatever it stores is disclosed to all of them:

- **No secrets.** Passwords, password hashes, API token values, bot tokens,
  webhook URLs, signing secrets and provider credentials are stripped before an
  entry is written.
- **No config payloads.** A config-as-code apply records the manifest name and
  how many checks were created, updated and deleted — never the manifest, which
  routinely carries secret references.
- **Changed field names, not always values.** An update entry lists the fields
  that moved, and shows before → after only for non-sensitive scalar values.
  Rotating a webhook secret shows up as "the secret changed", and nothing more.

## Failed sign-ins

Failed sign-ins are the one event a stranger can trigger at will, so they get
special handling — otherwise a credential-stuffing run would bury your real
audit trail under its own noise:

- Repeated failures for the same account from the same address, within a short
  window, collapse into **one entry with a counter** ("47 attempts between
  09:02 and 09:11"), which is also easier to read than 47 rows.
- An hourly per-organization ceiling caps how many failed-sign-in entries can
  be created. The counter lives in each server process's memory, so a
  multi-replica deployment enforces the ceiling **per replica** — still a hard
  bound, just N times the configured one.

A sign-in attempt that cannot be matched to an organization is not recorded —
there is no shared bucket a stranger can write into.

## Privacy and retention

| Setting | Environment variable | Default |
|---|---|---|
| Capture client IP addresses | `SP_AUDIT_CAPTURE_IP` | `true` |
| Retention window, in days | `SP_AUDIT_RETENTION_DAYS` | `365` |
| Failed sign-in fold window, in minutes | `SP_AUDIT_FAILED_LOGIN_FOLD_WINDOW_MINUTES` | `10` |
| Failed sign-ins recorded per org per hour | `SP_AUDIT_FAILED_LOGIN_MAX_PER_ORG_PER_HOUR` | `60` |

Set `SP_AUDIT_CAPTURE_IP=false` and no IP address is **stored** at all — not
merely hidden from the UI. This is the switch for deployments where client
addresses are personal data you would rather not hold.

A daily cleanup job removes entries older than the retention window. Setting
`SP_AUDIT_RETENTION_DAYS=0` keeps everything forever, which is what you want
under a legal hold.

## Reading it through the API

The audit log is the organization events endpoint:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  'https://solidping.io/api/v1/orgs/acme/events?type=auth,member&since=2026-08-01T00:00:00Z&limit=100'
```

- `type` filters by category (`auth`, `member`, `integration`, …)
- `eventType` filters by exact type (`member.role_changed`)
- `actorUserUid` filters to one person's actions
- `targetType` / `targetUid` filter to a kind of object, or one object
- `sourceIp` filters to one client address (admins and owners only)
- `since` / `until` bound the window (RFC3339)
- `cursor` pages through the result; hand `pagination.cursor` back verbatim

Authentication events, IP addresses and user agents are returned to org admins
and owners only — the same rule the dashboard follows, enforced server-side.

Streaming export to a SIEM (webhook or syslog) is not available yet.
