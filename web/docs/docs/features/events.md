---
sidebar_position: 16
title: Events
---

# Events

The **Events** page (`/orgs/:org/events`) is an org-wide, chronological
timeline of what happened to your checks and incidents — every member of the
organization can see it, not just admins. Each row shows when the event
happened, what it was, who (or what) triggered it, and a link to the related
check or incident.

## Event types

The type filter narrows the timeline to the most common operational events:

| Type | Meaning |
|---|---|
| `check.created` | A check was added |
| `check.updated` | A check's configuration changed |
| `check.deleted` | A check was removed |
| `incident.created` | A new incident opened |
| `incident.acknowledged` | An incident was acknowledged |
| `incident.escalated` | An incident escalated to the next step |
| `incident.resolved` | An incident resolved |

The `incident.escalated` event raised by an escalation policy step records what
was paged, with names captured when the step fired (a later rename does not
change old rows): `step_uid`, `step_position`, `repeat_index`, `policy_uid`,
`policy_name`, `check_name`, `check_slug`, and `targets`, a list (at most 20) of
`{type, uid, name}` entries where `type` is `integration`, `user`, `schedule`
(plus `on_call_user_uid` / `on_call_user_name` when resolved) or `all_admins`.
Targets skipped by the step's severity are not listed. The Events page shows
them as "Step 2 · Ops Slack, alice", integrations and schedules linked.

Leaving the filter on **All Events** shows the complete stream — every event
type the organization records, including status page publications,
membership and integration changes, and more — each with its own label and
badge. The dropdown's seven entries are shortcuts for the day-to-day noise,
not the full list of what can appear here.

Private-location agent rows (`agent.connected` / `agent.disconnected`) name
the agent under the label — its name and private location, plus the reason
it dropped (`ping timeout`, `revoked`, `server shutdown` or `error`) on a
disconnect — and the **Related** column links the agent to
**Organization → Private locations**.

Discovery scans record `discovery.scan_started` and `discovery.scan_canceled`
(with the type and, for a scan run from a private location, the location), and
`discovery.chunk_refused` when an agent refuses a chunk of a scan because the
range is not allowed or discovery is off.

The **Actor** column names the user, integration, or system process that
triggered the event — including a Slack, Discord, or phone acknowledgment,
which has no dashboard user behind it.

## How it differs from the audit log

Events and the [audit log](audit-log.md) are two views over the same
underlying record, aimed at different audiences:

- **Events** is for everyone in the organization, and is scoped to what you'd
  want to see day to day: checks and incidents, front and center, with a
  quick filter for the most common types.
- The **audit log** (Organization → Audit) is the admin/owner-only security
  and configuration trail — sign-ins, membership, tokens, integrations,
  escalation policies, and every other configuration change — with richer
  filtering (actor, target, source IP) and the provenance fields (client IP,
  user agent) that only admins and owners are allowed to see.

Any event you see on either page ultimately comes from the same
`GET /api/v1/orgs/:org/events` endpoint — see the [audit log](audit-log.md)
page for the full filtering reference.

## Incident events

An incident's own timeline (acknowledged, escalated, resolved, comments…) is
the same event stream, scoped to that one incident — see
[Incident Management → Events](incidents.md#events) for the incident-specific
view and its "Get Incident Events" endpoint.

## Private Atom feed

The Events page needs a session. For a feed reader, a NOC wall or an
automation hook, an org admin can create a **private Atom feed** of the same
events instead: **Organization → Settings → Event feeds**, or
`POST /api/v1/orgs/:org/event-feeds`.

The feed URL carries a secret and nothing else:

```
https://solidping.acme.com/api/v1/org-feeds/spf_<43 characters>/feed.xml
```

- **The URL is the credential.** Anyone who has it can read the feed, with no
  sign-in. The secret is shown once, when the feed is created or rotated;
  SolidPing only stores its SHA-256, so it cannot show it again. Treat the
  URL like a password (it starts with `spf_` so secret scanners can flag it).
- **What it contains**: the newest 100 events of the last 30 days, whichever
  is smaller, newest first, from the same query as the Events page. Each entry
  is titled with the Events page label (in the reader's `Accept-Language`,
  English by default) and the check it is about, and links to the incident or
  the check in the dashboard. Incidents never published to a status page are
  included.
- **Scope, per feed**: by default `incident.*` plus `check.created`,
  `check.updated` and `check.deleted`. A feed can also carry
  `status_update.*` and `maintenance_window.*`. Sign-in, membership, token and
  integration events are never offered: they belong to the
  [audit log](audit-log.md).
- **Several feeds per organization** (up to 10), each with its own name, so
  one consumer can be cut off without breaking the others. The settings card
  shows when each feed was last read.
- **Rotate** a feed to replace its secret, or **revoke** it. Either way the
  old URL answers `410 Gone`, the signal feed readers take to stop polling;
  an unknown URL answers the same. Creating, rotating and revoking a feed are
  written to the audit log (`org.settings_updated`, target type `event_feed`).
- The feed is rate limited per URL and sent with
  `Cache-Control: private, max-age=60` and `X-Robots-Tag: noindex`.

### How it differs from the status page feed

Each [status page](status-pages.md) also has a public Atom feed
(`/api/v1/status-pages/:org/:slug/feed.xml`). That one is for your
customers: it only shows the status updates you published on that page, and
it follows the page's visibility. The private event feed is for your own team:
it shows the raw incident and check events of the whole organization, whether
or not anything was published.
