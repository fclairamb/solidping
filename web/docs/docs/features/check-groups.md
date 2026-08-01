---
sidebar_position: 10
title: Check Groups
---

# Check Groups

Check groups let you organize related checks — everything behind one service, one
customer, or one environment — into a named collection. Groups keep large check lists
navigable and drive two behaviors that matter during an outage: **grouped pagination**
and **group-incident correlation**.

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

## Group-incident correlation

When several checks in the same group fail at once, SolidPing correlates them into a
single **group incident** instead of paging you once per check — one alert per outage,
not one per symptom. See [Incident Management](/features/incidents#group-incidents-correlated-outages)
for how correlated incidents behave.

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
