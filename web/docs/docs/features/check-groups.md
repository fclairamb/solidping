---
sidebar_position: 10
title: Check Groups
---

# Check Groups

Check groups let you organize related checks — everything behind one service, one
customer, or one environment — into a named collection. Groups keep large check lists
navigable and drive two behaviors that matter during an outage: **grouped pagination**
and **grouped incident display**.

## What a group is

A check group has a name, a URL-friendly slug, an optional description, and a sort
order. Each check optionally belongs to exactly one group via its `checkGroupUid`
(a check with no group is ungrouped). The group tracks how many checks it contains
(`checkCount`).

### The slug is the stable identifier for scripting

`GET`/`PATCH`/`DELETE` requests accept either the group's UID or its slug in the
URL, and the slug is what shows up in incident payloads as `checkGroupSlug`. That
makes the slug — not the UID — the identifier to put in DevOps scripts, CI jobs,
and other tooling that addresses a group by name. The dashboard's group edit page
and "New Group" dialog both let you set it directly (auto-derived from the name if
you leave it blank on create); changing an existing group's slug breaks anything
still using the old one, since there is no redirect from an old slug to a new one.

## Group status

Every group also carries a derived, read-time `status` plus a
`memberStatusCounts` breakdown (wire status → count), computed from its
**enabled** member checks — no new stored state, and nothing that changes
alerting (see [Grouped incident display](#group-incident-correlation) below).
Disabled and deleted checks never affect the rollup.

| Enabled members | Group status |
|---|---|
| None, or all `created` | `created` |
| All `down` | `down` |
| Some (not all) `down` | `degraded` |
| No `down`, at least one `warning` | `warning` |
| No `down`/`warning`, at least one `validating` | `validating` |
| Otherwise, at least one `up` | `up` |

This mirrors a check's own status vocabulary, so the same status colors and
labels apply — a group reads as one thing, not four.

## Group-level escalation policy

A group can carry its own escalation policy that member checks inherit. Escalation
resolution walks a chain, most specific first:

1. The **check's own** escalation policy, if set.
2. Otherwise the **group's** escalation policy, if the check belongs to a group that has one.
3. Otherwise the **organization default** policy, if configured.
4. Otherwise no escalation.

This means you can set paging behavior once per service group instead of per check,
and still override it on an individual check when needed.

## Grouped pagination

When checks are grouped, the dashboard paginates by group rather than flattening
everything into one long list, so you see a service's checks together and can page
through many checks without losing that structure.

## By-host view

Groups are whatever you made them — often organized by check *type* ("TLS
certificate expiry" for 40 hosts) rather than by host, so the grouping that best
matches real-world failure correlation — everything probing the same host — may not
exist anywhere in your group structure. The checks list's **Group by: Groups / Host**
toggle switches to a by-host view without requiring you to reorganize anything: every
check is bucketed by its derived `targetHost` — the config's `host` field when
present, else the hostname parsed from `url`, else `target` — with checks that have
none of those fields (e.g. heartbeat/email passive checks) in a trailing "No host"
section.

`targetHost` is **derived at read time, not stored**: it is recomputed from each
check's config on every response, so renaming a host in one check's config moves that
check to a different section the next time you load the page — there is nothing to
migrate or keep in sync. It has no effect on alerting, groups, or status pages; it is
purely a dashboard view. The checks list API also accepts `?sort=targetHost` if you
want to page through checks in host order yourself.

## Grouped incident display {#group-incident-correlation}

Incidents are always per-check: a member of a group that fails opens its own
incident and pages its own channels. The grouping shows up where it helps rather
than in the data — the dashboard lists a group's active incidents under a
*"RabbitMQ — 2/6 down"* header, and a group published as one status-page
component produces one public incident however many members are down. See
[Incident Management](/features/incidents#group-incidents-correlated-outages)
for the details, including what changed in v0.18.0.

## Managing groups

Create, list, update, and delete groups from the dashboard or the REST API:

```bash
# List groups
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:4000/api/v1/orgs/default/check-groups

# Create a group
curl -X POST http://localhost:4000/api/v1/orgs/default/check-groups \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Payments API","slug":"payments-api","description":"Everything behind checkout"}'
```

Assign a check to a group by setting its `checkGroupUid` when you create or update the
check.
