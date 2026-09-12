---
model: opus
effort: medium
---

# A `secrets: stripped` export still carries every email check's ingest token, and can emit a check its own import rejects

## Problem

Two things a config-as-code export must guarantee — *it contains no secret* and
*it re-imports as-is* — both broke on the first real refresh of a tracked org
(exp-devops `solidping/config.yaml`, PR stonal-tech/exp-devops#663, 2026-09-11).

### 1. The email ingest token is exported, twice

An `email` (passive) check's only config key is `token`: the local part of its
inbound address `<token>@<addressDomain>`. Whoever knows it can mail that address
and mark the check `up`. [`checkemail/secret_fields.go`](server/internal/checkers/checkemail/secret_fields.go)
declares it **not** a secret, on purpose and with a good reason:

> Inbound email is matched to its check with `GetCheckByEmailToken`, which
> queries `config->>'token'` on the public column … Splitting/redacting it (as a
> declared secret would) removes it from the public column and breaks inbound
> matching.

That is a fine storage decision and a wrong *export* decision. The export path
([`export_v2.go`](server/internal/handlers/checks/export_v2.go)) strips only
`SecretFields()`, so the document — stamped `secrets: stripped` — carries the
48-hex-char token verbatim. It then carries it a second time on any `smtp` check
whose `delivery_to` targets that inbox
([`checksmtp/config.go:59`](server/internal/checkers/checksmtp/config.go:59)),
because `delivery_to` is that same address.

The refresh commit in exp-devops#663 committed both. The repo's validator now
refuses those shapes and the two checks are excluded from the tracked file — a
workaround, not a fix: those checks are simply not under config-as-code any
more.

The heartbeat check has the identical problem and already solves it: its ping
token is preserved across imports by
[`preserveHeartbeatToken`](server/internal/handlers/checks/service.go:4819)
when the incoming config omits the key. Email got no such treatment, so today
the only two options for an email check are "commit the token" or "never import
the file".

### 2. An empty `name` is accepted, then exported as absent, then rejected

The instance held `domain-stonal-dev-io` with `name: ""`. The API accepted it
(create or PATCH — the validation treats an empty string as present), the
exporter omits empty strings, and the import path requires `name`. Result: the
org's own export failed the exp-devops validator with `missing required key
'name'`, and would have failed `POST /checks/import` too. A document the server
produced that the server cannot consume.

## Proposal

### Export redaction is a per-checker declaration, distinct from storage secrecy

- Add an optional `ExportRedactedFields() []string` to the checker config
  interface, alongside `SecretFields()`. Semantics: *public at rest, never
  exported*. `checkemail` declares `token`; `checksmtp` declares `delivery_to`
  (it is fully derivable from `delivery_check_uid`, which stays exported).
- `export_v2.go` strips `SecretFields() ∪ ExportRedactedFields()`. The
  `secrets: stripped` marker then means what it says.
- Import and apply preserve a redacted field when the incoming config omits it,
  exactly as `preserveHeartbeatToken` does — generalize that function into
  "preserve absent redacted fields" and make heartbeat's token the first user
  of the generic path (no behaviour change for heartbeat). For smtp,
  `delivery_to` absent + `delivery_check_uid` present → re-derive from the
  referenced check's token, which is what the config already documents as the
  relationship.
- `registry.TestNoUndeclaredCheckerSecrets` gets a sibling: for every
  registered checker, build the sample config, run it through the exporter, and
  assert no exported string value matches `^[0-9a-f]{32,}$` or
  `^[0-9a-f]{32,}@` — the shapes a minted token takes. This is the test that
  would have caught this before a customer's repo did.

### Reject empty names; never export an invalid document

- `name` is `required` and `min=1` after trimming on create and update
  (`VALIDATION_ERROR`, field `name`). The dashboard already requires it; only
  the API let it through.
- Backfill: a one-shot migration sets `name = slug` where `name = ''`, so the
  next export is valid without anyone hunting for nameless checks.
- Round-trip test: for every sample check, `export → ValidateDocument` reports
  zero issues, and `export → import(dryRun)` reports no errors. Today this
  test would fail on the empty-name case; after the migration it is the
  regression guard.

### Out of scope

- Rotating existing email tokens. A token that reached a git history is
  compromised in the usual sense; whether to rotate is the org's call and there
  is no rotation endpoint for email checks today (heartbeat has one). Worth its
  own small spec if wanted: `POST /checks/:uid/rotate-token` generalized from
  `RotateHeartbeatToken`.
- The `${env:}`/`${param:}` reference story (`2026-09-11-03`) and the
  round-trip / validate-endpoint story (`2026-09-11-04`).
