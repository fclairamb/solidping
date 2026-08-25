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

Leaving the filter on **All Events** shows the complete stream — every event
type the organization records, including status page publications,
membership and integration changes, and more — each with its own label and
badge. The dropdown's seven entries are shortcuts for the day-to-day noise,
not the full list of what can appear here.

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
