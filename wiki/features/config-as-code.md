# Config-as-code (declarative checks)

Manage an org's checks declaratively from a file in git: export the current
state, edit it, and `apply` it back. Apply is the **reconcile sibling** of
import — idempotent upsert-by-slug plus delete-by-absence within a bounded,
opted-in managed scope. The manifest is the existing export document shape, so
`export → edit → apply` round-trips with no separate schema.

This page covers the workflow and the CLI; the HTTP surface (request/response,
query flags, managed scope, secret references, deletion safety) is documented at
[`api-specification.md`](../api-specification.md) under
`POST /api/v1/orgs/:org/checks/apply`.

## The managed scope

Apply stamps every check it owns with a reserved label
`solidping.io/managed=<manifest-name>` (the manifest name is the document's
`organization` field, falling back to the org slug). The reconcile scope is
exactly the checks carrying that label. **Hand-created checks are never adopted,
modified destructively, or deleted** — they surface in the plan as `unmanaged`.

Plan actions (matched on `slug` within the managed scope):

| Action | Meaning |
|---|---|
| `create` | In the manifest, absent from the org. |
| `update` | Managed slug present in both. |
| `unmanaged` | Slug exists without the managed label — reported only. |
| `delete` | Managed check absent from the manifest (delete-by-absence). |
| `rename` | Manifest check with `previousSlug`/`uid` → rename in place. |

## Secret references

Never inline secrets. Config string values may reference `${env:NAME}` and
`${param:KEY}`, resolved **server-side at apply time** (env vars; the
`parameters` table — org-scoped first, then system-wide) into the existing
encrypted `config_private` envelope. The committed file stays secret-free. A
missing reference is a hard apply error. With `SP_ENCRYPTION_MASTER_KEY` unset,
a resolved reference emits a warning (the value lands in plaintext config).

```yaml
version: 1
organization: default
checks:
  - slug: api
    name: API
    type: http
    enabled: true
    config:
      url: https://api.example.com/health
      header_authorization: "Bearer ${param:api_token}"
```

## Deletion safety

Delete-by-absence happens **only** when all of: `--prune` is set, the check
carries the managed label, and the delete count is within the deletion cap
(default 10). Beyond the cap, apply refuses unless `--force`.

## Authorization

Apply, export, and import are all **admin-only**. (Export/import were
authentication-only before 2026-06-20 — see the back-compat note in the API
spec.)

## CLI

```bash
# Bootstrap a manifest from the current org state
sp checks export --file checks.json
#   …edit checks.json (or convert to YAML)…

# Preview the reconcile plan (mutates nothing)
sp apply -f checks.yaml --dry-run

# Apply (prints the plan, then prompts before mutating)
sp apply -f checks.yaml

# Apply non-interactively, allowing deletes of absent managed checks
sp apply -f checks.yaml --prune --yes

# Lift the deletion cap for a large prune
sp apply -f checks.yaml --prune --force --yes

# Plain idempotent import (no deletes, no managed scope)
sp checks import checks.json --dry-run
sp checks import checks.json
```

`sp apply` always computes a dry-run plan first and prints it; without `--yes`
it prompts for confirmation before applying. The file extension selects the body
format (`.json` → JSON, otherwise YAML). All three commands require an admin
session.
