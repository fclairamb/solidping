---
sidebar_position: 26
title: Check history
---

# Check history

SolidPing keeps a version history of every check. Each change to a check's
definition records a new version, whoever made it: the dashboard, the API,
`sp apply` or an import, or the MCP server. You can see what changed, who
changed it, and put an older version back.

Open it from the check page: **⋯ > History**.

## What is versioned

A version holds the check's definition:

- name, slug, description, type
- the public part of the config
- check group
- placement: pinned regions, or automatic placement with its region count and pool
- failure quorum
- enabled
- period
- labels

It does not hold:

- **Secrets.** Passwords, private keys, script secrets and any config key a
  check type declares secret are never stored in a version. A restore keeps
  the check's current secrets.
- **Export-redacted values**, such as a heartbeat or email check's ingest token.
- **Runtime state**: status, counters, schedule.
- The region list of an automatically placed check. The scheduler moves it on
  its own; the version records the placement settings instead.

A write that changes none of these (a secret rotation, a status update)
records nothing. A single edit that changes several things, for example the
name and the labels, records one version.

Checks created before the history existed get their first version on their
next edit, with the state they had just before it.

## Who made the change

Each version has an origin:

| Origin | Meaning |
|---|---|
| `user` | A dashboard session |
| `api` | An API token |
| `apply` | A config-as-code apply or import |
| `mcp` | The MCP server |
| `system` | No caller: background jobs, migrations |
| `ai_generate`, `ai_repair` | Proposed by an AI |

The user who made the change is shown next to it.

## Diff and restore

Select a version to see what it changed compared with the version before it.
Config and labels are compared key by key (`config.url`, `labels.env`).

**Restore** applies an older version again. It goes through the same
validation as an edit, and is itself recorded as a new version with the
reason `restored vN`.

## Proposed versions

A version can also be *proposed* instead of applied. Proposals are listed at
the top of the History page with **Approve** and **Reject**:

- **Approve** applies the proposal. It is refused (`409 CONFLICT`) when the
  check changed since the proposal was made: the proposal was built on an older
  version.
- **Reject** keeps the proposal in the history, marked rejected. The check is
  not changed.

## Retention

The last 100 applied versions of each check are kept. Deleting a check
deletes its history.

## API

| Method | Path | |
|---|---|---|
| `GET` | `/api/v1/orgs/{org}/checks/{uid}/versions` | Versions, newest first (`limit` supported) |
| `GET` | `/api/v1/orgs/{org}/checks/{uid}/versions/{n}` | One version with its snapshot |
| `GET` | `/api/v1/orgs/{org}/checks/{uid}/versions/{n}/diff?against={m}` | Field diff, against the previous applied version by default |
| `POST` | `/api/v1/orgs/{org}/checks/{uid}/versions/{n}/restore` | Restore an applied version |
| `POST` | `/api/v1/orgs/{org}/checks/{uid}/versions/{n}/approve` | Approve a proposal |
| `POST` | `/api/v1/orgs/{org}/checks/{uid}/versions/{n}/reject` | Reject a proposal |

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://solidping.acme.com/api/v1/orgs/default/checks/api-health/versions"
```

```json
{
  "data": [
    {
      "version": 2,
      "status": "applied",
      "origin": "user",
      "actorUserUid": "0190c3a2-...",
      "actorName": "Alice",
      "createdAt": "2026-10-03T09:12:44Z"
    },
    {
      "version": 1,
      "status": "applied",
      "origin": "apply",
      "actorUserUid": "0190c3a2-...",
      "createdAt": "2026-10-01T16:02:10Z"
    }
  ]
}
```

Every new version past the first also emits a `check.updated` event carrying
the `version` number. `check.created` carries `version: 1`.
