---
sidebar_position: 23
title: Config as Code
---

# Config as code

Keep an organization's checks in a file, in git, and let SolidPing reconcile
against it: export the current state, edit it, validate it in CI, and apply it
back. The file is the export document — there is no second schema to learn, and
no translation layer to drift.

```bash
sp checks export --file config.yaml   # bootstrap from live state
sp checks validate config.yaml        # offline: no token, no network
sp checks diff config.yaml            # has anything drifted?
sp apply -f config.yaml               # reconcile (prints the plan first)
```

## The round-trip guarantee

**A fresh export, fed straight back in, is a no-op.** That sentence is the
whole contract, and it is what makes everything else safe to build on:

```
export → validate  = 0 issues
export → dry run   = 0 created, 0 updated, 0 deleted, N unchanged
```

`created=0 updated=0 deleted=0` is the machine-readable *"the file matches the
instance"*. It is what a CI job asserts after a merge, and what tells you a
pull request really does change only what its diff shows.

The comparison is made on the **normalized effective** configuration, not on the
text: region spellings are folded, `expectedStatus` and `expectedStatusCodes`
are reconciled, periods are compared as durations, and document-level defaults
are resolved on both sides. So a difference reported in a plan is a real
difference.

An `update` entry names the fields that move:

```json
{"slug": "api", "action": "update",
 "changes": [{"field": "config.url",
              "from": "\"https://acme.com/api\"",
              "to": "\"https://acme.com/api-v2\""}]}
```

Secret values, and any value containing a `${param:…}` / `${env:…}` reference,
are masked as `***` (a generated `${var:…}` value is not a secret and is shown
as is) — a plan gets pasted into tickets and CI logs, and must
never be the thing that publishes a credential.

### `${env:}` references need the operator allowlist

A `${env:NAME}` reference only resolves for a name listed in
`SP_CHECK_ENV_ALLOWLIST` (comma-separated, empty by default) on the process that
runs the check — the server, and each private-location agent for its own checks.
`sp checks import` and `sp apply`, `--dry-run` included, refuse a file whose
`${env:NAME}` the server does not list, or lists but does not set, with
`VALIDATION_ERROR`; the two cases fail identically, so a plan never reveals
whether a server variable exists. Prefer `${param:…}` for anything the
organization should manage itself. Details:
[`${env:}` only reads allowlisted variables](/cli#env-allowlist).

:::warning Upgrading to a version with the allowlist
Existing `${env:NAME}` references are kept, but stop resolving until the name is
listed: those checks go red with `unresolved secret reference: env:NAME` on
their next run, and re-applying a file that carries them is refused. Set
`SP_CHECK_ENV_ALLOWLIST` on the server and on every private-location agent
before or right after upgrading. No migration is needed. See the
[upgrade note](/cli#env-allowlist) for the secret-rotation advice.
:::

### Two things a plan will tell you about

**`unmanaged` is drift, not agreement.** It means the check exists but no
manifest owns it yet. For an organization that has never run `sp apply`, *every*
check is unmanaged — so an unmanaged entry carries its field diff like any
other, and `sp checks diff` exits non-zero for a file that does not match, no
matter who owns the checks.

**`escalationThreshold` is reported but cannot be applied.** The export carries
it, but no write request does, so editing it in the tracked file changes nothing
on the instance. The plan reports the difference and warns, naming the field and
the checks — rather than answering `unchanged` for a file that genuinely
differs. Everything else in the document applies normally.

## Canonical spellings

A file and a server can write the same thing two ways. Where that is possible,
SolidPing accepts both and emits one:

| | Written by export | Also accepted |
|---|---|---|
| Private location | `@paris` | `@acme/paris` |
| HTTP status expectation | `expectedStatusCodes: [200]` | `expectedStatus: 200` |

**The folded `@location` is canonical.** `@acme/paris` (the older
fully-qualified form) is normalized to `@paris` on the way in when `acme` is
your own organization, and refused when it names somebody else's. The two are
*the same region*: a file written the long way plans as `unchanged`, never as a
change.

**`expectedStatusCodes` supersedes `expectedStatus`.** Setting both is an
error rather than a silent preference, because the loser would be dead config
that still looks meaningful.

## Region placement

An automatically placed check exports as `placement: auto` with its
`regionCount` and `regionPool`, and no `regions` (they are the scheduler's
current choice, not part of the document). `placement: auto` with no `regions`
switches an existing pinned check to automatic placement; an explicit `regions`
list pins a check. See [Region placement](./region-placement.md).

`failQuorum` (`all`, `majority` or a number) is exported only when it is not
the default. A document that leaves it out puts the check back on the default,
and the plan shows that change.

## What `secrets: stripped` means

Every export carries `secrets: stripped`, and it is a promise about two sets of
keys:

- **Declared secrets** — passwords, private keys, `secretHeaders`, `basicAuth`,
  a JS check's `secrets` map. These live encrypted and never appear in any API
  response either.
- **Export-redacted fields** — keys that are perfectly ordinary at rest but must
  not reach a committed file: an email check's ingest token, and an SMTP probe's
  `delivery_to` (which contains one).

Both are **restored on import**, which is why a stripped export re-imports
cleanly and why re-applying one never rotates a token or breaks a paired
check. A file marked `secrets: stripped` is not an incomplete file, and the
validator does not treat it as one.

A secret you *do* want in version control belongs in a reference:

```yaml
config:
  url: https://sso.acme.com/token
  method: POST
  body: "grant_type=password&username=probe&password=${param:sso-authtest-password}"
```

The reference is what is stored and what comes back out; the value is resolved
when the check runs. Manage those values under **Organization → Variables** or
with `sp params set`.

### Generated values

A `${var:NAME}` reference is replaced by a value SolidPing generates on every
run. Use it to get past a CDN or a caching proxy that would otherwise answer
with a cached 200 while the origin is down.

| Name | Value | Example |
|------|-------|---------|
| `${var:timestamp}` | UTC time, 17 digits `YYYYMMDDhhmmssSSS` | `20261012143005123` |
| `${var:iso8601}` | UTC time to the second | `2026-10-12T14:30:05Z` |
| `${var:uuid}` | Random UUID (v4) | `3f6c1a2e-8b4d-4f0a-9c1e-2d7b5a9e0f13` |
| `${var:random}` | 8 characters from `a-z0-9` | `k3x9q0az` |

```yaml
config:
  url: https://acme.com/health?cb=${var:timestamp}
  headers:
    X-Request-Id: ${var:uuid}
```

- Every occurrence of a name gets the same value within one run, and a new
  value on the next run. Each region gets its own.
- Names are lowercase. An unknown name (`${var:UUID}`, `${var:foo}`) is refused
  when the check is saved or validated.
- The result stores the URL with the generated values. A `${param:…}` in the
  same URL stays a reference there, never the secret.
- A deported agent older than this release does not know `${var:…}`: its runs
  report `unresolved secret reference: var:…` as an error result. Upgrade the
  agent first.

## Validating in CI

`POST /api/v1/orgs/:org/checks/validate` accepts a whole document — JSON or
YAML — and answers with **every** problem at once:

```json
{
  "valid": false,
  "issues": [
    {"slug": "api", "field": "regions", "code": "REGION_FORMAT",
     "message": "region \"Paris!\" must be a slug or \"@private-location\""}
  ]
}
```

`code` is stable and `message` is prose, so a pipeline branches on the code and
never on the wording. Any organization **member** may call it — validating
changes nothing, and a job that only asks "is this file valid?" should not need
a token that could delete checks. Add `?plan=true` (admin) to get the reconcile
plan back in the same call.

Offline, with no token at all:

```bash
sp checks validate config.yaml
```

This runs the server's own rules, shipped in the same binary — which is the
point. A validator that re-implements the rules falls behind the day a check
type is added, and then reports the server's own export as broken.

Two rules need your organization and so are answered only by the endpoint: does
a `${param:…}` reference resolve, and does the check already exist. The second
matters because a `secrets: stripped` document legitimately omits a declared
secret for a check that exists (the import restores it) and illegitimately for
one that does not. Offline, `sp checks validate` assumes the check exists; post
the file to the endpoint when you want the answer the write path will give.

Get `sp` into a pipeline from the release assets or the image — see
[the CLI page](../cli.md#installing).

## Reconciling, and deleting safely

`sp apply` adds what `import` deliberately does not: **delete-by-absence**,
inside a scope you opt into. Applying stamps every check it owns with a reserved
label, and only checks carrying that label are ever deleted. Anything you
created by hand shows up in the plan as `unmanaged` and is left alone.

Deleting further requires `--prune`, and is capped (10 by default) unless you
pass `--force` — a bad manifest cannot quietly wipe a fleet.

```bash
sp apply -f config.yaml --dry-run          # plan only
sp apply -f config.yaml                    # prompts, then applies
sp apply -f config.yaml --prune --yes      # non-interactive, deletes by absence
```
