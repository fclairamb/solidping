---
sidebar_position: 9
title: Pre-filled "Add check" links
---

# Pre-filled "Add check" links

The check **create** page can be pre-filled from URL query parameters, so you
can hand someone a single link that opens the new-check form with the target,
schedule, and metadata already populated. This is ideal for "Add this to
monitoring" buttons in your READMEs, runbooks, CLI output, and onboarding
empty-states.

The form is only **pre-filled** — nothing is saved until the user reviews the
values and clicks **Create check**. Pre-filling applies to the create page only
(`/checks/new`); the edit page never reads query parameters.

## URL shape

```
https://solidping.io/dash0/orgs/<org>/checks/new?<params>
```

Replace `<org>` with your organization slug, and `solidping.io` with your own
host if you self-host.

## Supported parameters

| Parameter | Example | Maps to |
| --- | --- | --- |
| `checkType` | `checkType=http` | Check type |
| `checkName` | `checkName=API%20health` | Name |
| `checkSlug` | `checkSlug=api-health` | Slug |
| `checkPeriod` | `checkPeriod=60` | Check interval, in **seconds** |
| `httpUrl` / `url` | `httpUrl=https://example.com` | Target URL |
| `httpMethod` | `httpMethod=POST` | HTTP method |
| `host` | `host=db.example.com` | Host |
| `port` | `port=5432` | Port |
| `domain` | `domain=example.com` | Domain |
| `username` | `username=probe` | Username (HTTP: prefills the Basic Auth username) |
| `database` | `database=app` | Database |
| `expectedStatus` | `expectedStatus=204` | Expected HTTP status |
| `timeout` | `timeout=10` | Per-probe timeout, in **seconds** (1–30) |
| `label` | `label=env:prod` | Label(s) — see below |
| `region` | `region=us1,eu2` | Regions — see below |
| `group` | `group=production` | Group, by its **slug** |
| `confirmationPeriod` | `confirmationPeriod=120` | Incident confirmation period, in **seconds** |
| `recoveryPeriod` | `recoveryPeriod=120` | Incident recovery period, in **seconds** |
| `section` | `section=flapping` | Expand + scroll to a collapsible section |

### Labels

Labels use `key:value` pairs. Repeat the parameter, or comma-separate the
values, or both:

```
?label=env:prod&label=team:infra
?label=env:prod,team:infra
```

### Regions

Regions are given by slug. Comma-separate or repeat the singular `region`
parameter:

```
?region=us1,eu2
?region=us1&region=eu2
```

### Deep-linking a section

The create form groups less-common options into collapsible sections. Pass
`section=<name>` to open one and scroll to it on load — handy when a link is
specifically about, say, flapping tuning:

```
?section=flapping
```

Section names: `organization`, `dependencies`, `incident-tracking`,
`flapping`, `advanced`.

## Example

Monitor an HTTP endpoint every 30 seconds, expecting a `204`, tagged
`env:staging`, in a group called `edge`:

```
https://solidping.io/dash0/orgs/acme/checks/new?checkType=http&url=https://staging.example.com/health&checkPeriod=30&expectedStatus=204&label=env:staging&group=edge
```

Unknown or malformed parameters are ignored, and an unrecognized `group` slug
simply leaves the group unset — the form still opens.
