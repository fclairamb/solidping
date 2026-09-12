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
are masked as `***` — a plan gets pasted into tickets and CI logs, and must
never be the thing that publishes a credential.

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

## What `secrets: stripped` means

Every export carries `secrets: stripped`, and it is a promise about two sets of
keys:

- **Declared secrets** — passwords, private keys, `secretHeaders`, `basicAuth`.
  These live encrypted and never appear in any API response either.
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
when the check runs. Manage those values under **Organization → Parameters** or
with `sp params set`.

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
