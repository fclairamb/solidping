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

## The canonical spellings

A round trip only works if both sides agree on how a value is written. Three
places where the file and the server can legitimately differ — and what the
server does about each:

| | Canonical (what export emits) | Also accepted on input |
|---|---|---|
| Private location | **`@paris`** | `@acme/paris` (the pre-2026-08-13 form) |
| HTTP status expectation | **`expectedStatusCodes: [200]`** | `expectedStatus` (legacy; never both) |
| Secrets | **`secrets: stripped`** | `_secretsStripped: true` (v1) |

**The folded `@location` is the canonical form.** The server stores and exports
it; the long `@org/location` is normalized to it on the way in (for your own
org) or refused (for anybody else's). Both are *the same region*, so a manifest
written the long way plans as `unchanged`, not as a change. This is not a
detail: on the first tracked org, 183 of 197 reported "errors" were exactly
this difference, and every one of them was a validator concluding the server's
own export was wrong.

**`expectedStatusCodes` supersedes `expectedStatus`.** Setting both is an error
(`STATUS_FIELD_CONFLICT`), because the second one is then dead config that reads
as if it does something.

### What `secrets: stripped` guarantees

Since spec 2026-09-11-02 the exporter removes **`SecretFields()` ∪
`ExportRedactedFields()`** from every check's config:

- `SecretFields()` — what the checker declares secret (passwords, private keys,
  `secretHeaders`, `basicAuth`, …). These live in the encrypted `config_private`
  column and never appear in any API response either.
- `ExportRedactedFields()` — keys that are **public at rest** but must never
  reach a committed file: an email check's ingest token, and an SMTP probe's
  `delivery_to` (which embeds one). Before that spec a document stamped
  `secrets: stripped` carried a live 48-hex-char ingest token, twice.

**Both sets are restored on import**, so the round trip loses nothing: a secret
absent from the patch is preserved by the merge, and a redacted field is either
preserved from the stored check or derived from what the document did carry
(an SMTP probe's `delivery_to` is rebuilt from its `delivery_check_uid`). A
`secrets: stripped` document is therefore *not* incomplete, and the validator
does not treat it as such — a checker complaining that a stripped key is
missing is suppressed by parameter name on such a document.

What this does NOT mean: a secret you want under version control belongs in a
`${param:…}` reference (below). The reference is stored verbatim and travels in
the file; only the value stays out.

## Validating a file, without a write token

`POST /api/v1/orgs/:org/checks/validate` takes a whole document (JSON or YAML)
and answers with **every** problem, each carrying a stable `code` a CI job can
allow-list. It is **member-level**: validating writes nothing, and a pipeline
that only asks "is this file valid?" should not need a token that can delete
checks. With `?plan=true` (admin) it also returns the reconcile plan.

The full code list and the response shape are in
[`api-specification/checks.md`](../api-specification/checks.md#validating-a-whole-document).

Offline, with no token and no network at all:

```bash
sp checks validate config.yaml
```

That is **the** validator. It runs the server's own `ValidateDocument` — the
same function `/import`, `/apply` and the endpoint above run — so it cannot
drift from the server the way a re-implementation must.

Two things need the organization and so are answered only by the endpoint, never
offline: whether a `${param:…}` reference resolves, and whether a check the
document describes already exists (which is what decides if an omitted declared
secret is a legitimate `secrets: stripped` omission or a create that `/import`
will refuse). The offline validator assumes the check exists — the only
assumption under which an export validates with no network — so post the file to
`/checks/validate` when you want the gate to match what the write path will do.

An org-specific convention — stack roots, per-environment symmetry, naming
policy — is genuinely not the server's business and belongs in whatever tooling
owns that convention. The *format* rules do not.

### Getting `sp` into CI

Every tag publishes four archives (`darwin`/`linux` × `amd64`/`arm64`) plus a
`sp_<version>_checksums.txt`, and a `ghcr.io/fclairamb/solidping/sp` image:

```yaml
- name: Validate the SolidPing manifest
  run: |
    curl -sSL -o sp.tar.gz \
      https://github.com/fclairamb/solidping/releases/latest/download/sp_${SP_VERSION}_linux_amd64.tar.gz
    tar -xzf sp.tar.gz
    ./sp checks validate solidping/config.yaml
```

or, with no download at all:

```bash
docker run --rm -v "$PWD:/w" -w /w ghcr.io/fclairamb/solidping/sp \
  checks validate config.yaml
```

Exit 0 = valid, 1 = problems (each printed as `[slug] CODE field: message`),
≥2 = the file could not be read or parsed.

## Is the file still what is deployed?

```bash
sp checks diff config.yaml
```

It asks the server for the reconcile plan (a dry run that mutates nothing) and
prints one row per check — `create`, `update` with the fields that move,
`unchanged`, `delete`, `unmanaged` — then exits 0 (no drift) / 1 (drift) /
≥2 (error). `--text` renders the old textual diff instead, which is also the
automatic fallback when the caller cannot plan (plans are admin-only).

`created=0 updated=0 deleted=0 unmanaged=0` is the answer: the file matches the
instance. Before spec 2026-09-11-04 it could not be obtained from the server at
all — `import --dry-run` counted every matched slug as an update, so
re-importing a byte-for-byte copy of the current export reported `updated=482`
— and every tool that needed the answer built its own normalizer and drifted.

**`unmanaged` counts as drift.** It answers "who owns this check?", never "does
it match?", and for an organization that has never run `apply` *every* check is
unmanaged — so treating it as agreement made `sp checks diff` print "No drift"
for a file that disagreed with all of them. Unmanaged entries carry their field
diff like any other, and the command says how many there are.

### The one field a plan cannot apply

`escalationThreshold` is exported but is not carried by any write request, so
editing it in a tracked file changes nothing. The plan reports the difference
and adds a warning naming the field and the slugs; it will keep reporting it
until the field becomes writable. That is deliberate — the alternative is
answering `unchanged` for a file that differs, which is the failure mode this
whole page exists to remove.

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
| `update` | Managed slug present in both, and a field moves — the entry carries `changes: [{field, from, to}]`, secrets and `${…}` references masked. |
| `unchanged` | Managed slug present in both, nothing moves. |
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
never "read it back". The `sp.` prefix is reserved for SolidPing and refused
with a 400.

**`${param:}` reads the referencing organization's own parameters, and nothing
else.** Two properties make that true structurally rather than by a list of
forbidden names:

- Org-managed parameters are **stored in their own namespace** (`usr.`, never
  visible in the API, the CLI or a reference). The platform's per-org rows — the
  wrapped encryption DEK, the registration policy, the session ceiling — are not
  in it, so no key an org admin can name reaches them. An org may even create a
  parameter *called* `encryption.dek`; it is a different row, and it resolves to
  their value.
- There is **no system-wide fallback**. `${param:}` used to fall back to the
  system `parameters` table when the org had no such key, which made every
  instance credential in it — the Teams app secret, the PostHog API keys, the
  Telegram webhook secret, the SMTP password — readable by any org admin who
  could write a check config and point it at a URL they controlled.

This replaced a denylist of platform key prefixes, which was already missing
four instance credentials on the day it was written. A denylist over a namespace
other people keep adding to is not a boundary.

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

Apply, export, and import are all **admin-only** — they mutate the whole check
set, and apply can delete by absence. (Export/import were authentication-only
before 2026-06-20 — see the back-compat note in the API spec.)

`POST /checks/validate` is the deliberate exception, and only for a **document**
body: it writes nothing, so it sits at `viewer`. The single-check form of the
same route keeps the write floor it always had, and `?plan=true` needs admin
because the plan reads the org's whole check set. Relaxing validate did not
relax its neighbours; `TestImportAndApplyStayAdminOnly` is what says so.

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
