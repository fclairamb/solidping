---
sidebar_position: 25
title: Labels
---

# Labels

A label is a `key=value` tag you attach to a check. A check can carry any
number of them, they are free-form, and they are scoped to your organization.
Labels do two things: they **filter** the checks list and the API, and they can
**select checks onto a status page** through a section membership rule.

```
env=prod        team=payments        tier=1        public=true
```

## Groups vs labels

A check belongs to **one group** and carries **any number of labels**. Use a
group for "what this check is part of" — it drives escalation, incident
correlation and status-page rollups. Use labels for "how I want to slice the
list" — they filter, and they can select checks onto a status page.

| | [Check groups](./check-groups.md) | Labels |
|---|---|---|
| How many per check | 0 or 1 — exclusive | any number |
| Shape | a named entity: name, slug, description, sort order | free `key=value` pairs |
| Organizes the checks list | yes — the list is paginated and rendered group by group | no — filter only |
| Escalation policy | a group can carry one that its members inherit | never |
| Incident correlation | yes — a group's incidents are shown together | no |
| Status pages | publish a whole group as one component | select checks into a section by label |
| SLOs, maintenance windows | can be scoped to a group | no |

If you are choosing between them: a group is an *organizational* decision that
changes behaviour, a label is a *descriptive* one that changes what you can find.
Most organizations end up using both — a check lives in the `Payments API`
group and carries `env=prod`, `team=payments`.

## Key and value rules

| | Rule |
|---|---|
| Key | lowercase, starts with a letter, then letters, digits or hyphens; 3 to 51 characters |
| Value | non-empty, at most 200 characters |

Formally the key must match `^[a-z][a-z0-9-]{2,50}$`. So `env`, `team`,
`cost-center` are fine; `os` (too short), `1abc` (leading digit),
`k8s.cluster` (dot) and `Env` (uppercase) are refused.

The same rule is enforced in three places — the dashboard as you type, the Go
validator before any write, and a `CHECK` constraint on both the SQLite and
PostgreSQL schemas — so a label that a config file accepts is a label the
database accepts. Breaking the rule answers `VALIDATION_ERROR`, naming the
offending key and the rule.

## Filtering

Label filters are **AND**, and values are exact. There is no wildcard and no
partial match.

```bash
# Checks that are BOTH env=prod AND team=payments
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks?labels=env:prod,team:payments'
```

A check labelled `env=staging` never matches `env=prod`. If you want "prod or
staging", make it two requests, or use a label whose value you control, such as
`tier=1`.

The dashboard's checks list exposes the same filter, and keeps it in the URL, so
a filtered list is a link you can share or bookmark.

## Autocomplete

`GET /orgs/:org/labels` answers what labels your organization actually uses, so
a UI or a script can suggest instead of guess.

```bash
# Distinct keys in use, most-used first
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/labels'

# Distinct values for one key
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/labels?key=env'
```

Both forms accept `q` (a prefix filter) and `limit` (1-200), and each entry
carries the number of distinct checks using it. The CLI wraps the same endpoint:

```bash
sp labels list
sp labels list --key env
```

## Setting labels

### In the dashboard

The check form has a **Labels** field, on the check itself, next to **Group**.
Type a key, then a value; existing keys and values are suggested from the
autocomplete endpoint above.

### Through the API

Labels are a plain map on the check. `PATCH` replaces the whole map, so send
every label you want the check to keep:

```bash
curl -X PATCH http://localhost:4000/api/v1/orgs/default/checks/payments-api \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"labels": {"env": "prod", "team": "payments", "public": "true"}}'
```

### In config as code

The export document carries `labels` as a map under each check, so labels are
reviewed in a pull request like everything else:

```yaml
checks:
  - slug: payments-api
    type: http
    config:
      url: https://acme.com/api/health
    labels:
      env: prod
      team: payments
      public: "true"
```

`sp apply -f config.yaml` reconciles them. A label present on the instance but
absent from the file is removed on the checks the manifest owns — which is the
point of [config as code](./config-as-code.md), but worth knowing before your
first apply.

Quote `"true"` and other YAML scalars that would otherwise parse as a boolean
or a number: a label value is always a string.

## Publishing checks with a label

A [status page](./status-pages.md) section can carry a membership rule, and the
useful rule is **By label**: every check carrying all of the given `key=value`
pairs becomes a component in that section, now and in the future.

The recommended pattern is an opt-in label you control, such as `public=true`.
It inverts the risk: with an "all checks" rule, a check you create is published
unless you remember to stop it; with a label, a check is private until someone
deliberately adds the label — and the publish decision lives on the check, next
to the person who knows whether the service is safe to name.

Internal checks are never matched by a membership rule, whatever labels they
carry.

A rule on a **public** page publishes matching checks to the public internet,
including ones created later. Read
[Dynamic sections](./status-pages.md#dynamic-sections) before enabling one.

## Where labels do *not* apply

Labels are descriptive; nothing alerts on them. They do not affect escalation,
incident correlation, SLO scope or maintenance-window targeting — all of those
are addressed by check or by [group](./check-groups.md).
