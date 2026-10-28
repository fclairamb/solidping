---
sidebar_position: 11
title: Check Dependencies
---

# Check Dependencies

When a core service goes down, everything built on it goes down too. Without
dependencies you get one page per check: "the database is down", then "the API
is down", "the dashboard is down", "the worker is down". A **dependency** tells
SolidPing that one check relies on another, so a cascade pages **once**, for
the root cause.

## Depends on, depended on by

A dependency links a **parent** check to a **child** check: the child depends on
the parent. The **Dependencies** card of a check's page shows both directions:

- **Depends on**: the parents of this check. When they break, this check is
  expected to break too.
- **Depended on by**: the children of this check. They break when this one
  breaks.

Use **Edit** on the card to add or remove parents. Dependencies cannot form a
cycle: a check can never end up depending on itself, directly or through
other checks.

## Hard and soft dependencies

Each dependency has a kind:

| Kind | What it means | Effect on paging |
|------|---------------|------------------|
| **Hard** | A failure of the parent causes the failure of the child. | While the parent has an open incident, the child's incident is still created, but it **does not page**: it is rolled up under the parent's incident. |
| **Soft** | The two checks are related, but neither causes the other. | None. The child pages normally. Soft dependencies document the relationship and appear in the dependency graph. |

## How rollup works

When a child check opens an incident while a hard parent (or a parent of a
parent) has an open incident started shortly before, the child's incident is
attached to it and its notifications are suppressed. It works in both
directions: if the parent confirms its outage **after** its children, their
incidents are rolled up at that moment, and the escalation steps that have not
fired yet are cancelled.

While a hard parent is still confirming a failure, a failing child waits for it
before opening its incident, so the parent's report goes out first and alone.
The **Depends on** list shows a warning when a child confirms much faster than
its parent can detect the same outage: nothing is broken, but a page on the
child can then arrive later than its own confirmation period suggests.

When the parent recovers:

- a child that recovered too stays silent: it never pages for this outage;
- a child that is **still failing** has a problem of its own: its incident
  starts paging normally.

Maintenance windows still apply: a child in maintenance opens no incident at
all.

## Declaring dependencies as code

In a [config-as-code](./config-as-code.md) document (excerpt), a check lists
its parents by slug:

```yaml
checks:
  - slug: api
    type: http
    config:
      url: https://api.acme.com/health
    dependsOn:
      - parentSlug: database
        kind: hard
      - parentSlug: cache
        kind: soft
        description: Slower without it
```

The same edges can be managed through the API:
`GET`, `POST`, `PATCH` and `DELETE` on
`/api/v1/orgs/{org}/checks/{check}/dependencies`, and the whole org's graph with
`GET /api/v1/orgs/{org}/dependencies`.
