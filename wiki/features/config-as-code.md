# Config-as-code (declarative checks)

Manage an org's checks declaratively from a file in git: export the current
state, edit it, and `apply` it back. Apply is the **reconcile sibling** of
import — idempotent upsert-by-slug plus delete-by-absence within a bounded,
opted-in managed scope. The manifest is the existing export document shape, so
`export → edit → apply` round-trips with no separate schema.

This page covers the workflow and the CLI; the HTTP surface (request/response,
query flags, managed scope, secret references, deletion safety) is documented at
[`api-specification/checks.md`](../api-specification/checks.md) under
`POST /api/v1/orgs/:org/checks/apply`.

## The managed scope

Apply stamps every check it owns with a reserved label
`solidping-managed=<manifest-name>` (the manifest name is the document's
`organization` field, falling back to the org slug). The reconcile scope is
exactly the checks carrying that label. **Hand-created checks are never adopted,
modified destructively, or deleted** — they surface in the plan as `unmanaged`.

> **Renamed 2026-09-10 (spec 2026-09-10-01).** The key was
> `solidping.io/managed`. A dot and a slash are not storable label key
> characters — the Postgres `labels_key_check` CHECK
> (`^[a-z][a-z0-9-]{2,50}$`) has refused them since the 001 baseline, so apply
> and every importer that stamps this label were broken on Postgres and only
> ever appeared to work against the laxer SQLite backend the test suites use.
> SQLite rows carrying the old key are renamed in place by migration `021`, so
> an existing deployment keeps its managed scope.

Plan actions (matched on `slug` within the managed scope):

| Action | Meaning |
|---|---|
| `create` | In the manifest, absent from the org. |
| `update` | Managed slug present in both. |
| `unmanaged` | Slug exists without the managed label — reported only. |
| `delete` | Managed check absent from the manifest (delete-by-absence). |
| `rename` | Manifest check with `previousSlug`/`uid` → rename in place. |

## Secret references

Never inline secrets. Any config **string value** may reference `${env:NAME}` or
`${param:KEY}`.

**The reference is what is stored.** Since spec 2026-09-11-03 the write path
(import and apply alike) only *validates* that a reference resolves; the value
is materialized at **execution** time and is never written to the `config`
column, never enveloped, and never returned by `GET /checks/:uid` or
`/checks/export`. So the committed file, the API response and the stored row all
show the same thing:

```yaml
config:
  url: https://sso.acme.com/token
  method: POST
  body: "grant_type=password&username=probe&password=${param:sso-authtest-password}"
```

That is what makes `export → edit → apply` round-trip with no redaction logic.
Before the change the resolved value was substituted into the config at apply
time, and only keys inside `SecretFields()` were split into `config_private` —
so a password resolved into a `body` (which is not a secret field) was stored in
the **public** config and served straight back.

### The two schemes

| | `${param:KEY}` | `${env:NAME}` |
|---|---|---|
| Managed by | An org admin, over the API | The operator, on the process |
| Where it lives | The org's `parameters` table | An environment variable |
| Changing it | `sp params set …`, takes effect next run | A redeploy / restart |
| Resolved on | The **API**, at claim/dispatch | The **executing process** |
| Good for | SaaS, and anything you want to rotate | Self-hosted, and per-region values |

`param:` is the SaaS-grade form. Manage it under **Organization → Parameters**,
or from the CLI:

```bash
sp params set sso-authtest-password 'hunter2'   # secret by default
sp params set region-label paris --public       # a plain, readable setting
sp params list                                  # secret values are never shown
sp params delete sso-authtest-password
```

Keys match `^[a-z][a-z0-9_.-]{0,63}$`. A `secret: true` parameter is
**write-only**: no endpoint returns its value, so rotation is "set it again",
never "read it back". Keys SolidPing owns for its own per-org configuration —
the `sp.` prefix, plus `encryption.`, `auth.`, `registration.` and friends — are
refused with a 400, and are equally unresolvable through `${param:}`: an org
must not be able to read its own wrapped encryption key, or the instance's SMTP
password, out through a check body.

`${env:}` resolves on **whichever process executes the check**. For a check
running on a deported agent that is the *agent's* environment, not the API's —
which is a feature (per-region credentials, and the value never leaves the
region) but is easy to be surprised by. Both endpoints warn once per document
when a manifest uses `env:`.

### Failure is visible, never silent

- **At write time** — an unresolvable reference fails the whole request with
  `400 VALIDATION_ERROR` before any mutation, from `/import` and `/apply`
  equally, dry run included.
- **At execution** — a reference that cannot be resolved (a deleted parameter,
  an unset variable) produces a check **result** with `status: error` and the
  output `unresolved secret reference: param:…`, visible in the check's history.
  The literal `${param:…}` is never sent to the target: against an endpoint that
  does not enforce the credential, that would have looked green.

### Where a resolved value is allowed to exist

Only in memory, on the process running the check, and inside the sealed payload
on the way there:

- **In-process worker** — resolved at claim, merged into the config just before
  the checker parses it. It is deliberately kept out of `job.Config` until then,
  so the worker's own "Executing check job" log line prints the reference.
- **System agents** — the `param:` values are resolved on the API and folded
  into the **sealed** envelope, which the agent merges over the reference still
  sitting in its public wire config. Nothing crosses the wire in the clear.
- **Private-region agents** hold their own key, and the server cannot open their
  verbatim `config_sealed` envelope. `${param:}` is therefore not available for
  a check running in a private region — use `${env:}` on the agent, which is the
  per-region form anyway.

With `SP_ENCRYPTION_MASTER_KEY` unset nothing changes for references: the
reference is stored either way, and there is no resolved value at rest to
protect.

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
