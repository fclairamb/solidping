---
model: opus
effort: medium
---

# An email check's inbound token can be set by the caller and is not unique, so a check in another org can share a victim's inbound address

## Problem

Found by the security scan of 2026-11-14 (finding AM1). Severity: low. Who can trigger it:
a `user` member of any org, so on SaaS anyone who signs up. They also need to know the 48-hex
token of a check in another org. That token is the secret part of the victim's inbound address,
and every system that mails the address knows it.

The token is meant to be server-generated and globally unique. Inbound mail is matched to a
check by the token alone, with no org in the lookup:
- `server/internal/db/postgres/postgres.go:1913-1926` and `server/internal/db/sqlite/sqlite.go:1896-1909`
  (`GetCheckByEmailToken`), called from `server/internal/handlers/emailcheck/handler.go:176`.
- The index `idx_checks_email_token` is not unique (`postgres/migrations/001_v0_1_0.up.sql:343`).
- The original spec said a duplicate must be refused before insert
  (`specs/done/2026/04/2026-04-29-02-email-passive-checks.md:210`). That check was never written.

But a caller can choose the token:
- `server/internal/checkers/checkemail/config/validate.go:25-32` generates a token only when
  `config.token` is absent or empty. Any other string is kept as is, on create and on update.
- `token` is not a secret field (`checkemail/config/secret_fields.go:20-22`), so in
  `mergePatchConfig` (`server/internal/handlers/checks/service.go:6072`) a `PATCH` that sends
  `config.token` overwrites it.
- Config-as-code import and apply keep a token that the document carries.

Impact, when two live email checks share a token:
- bun scans the first row and drops the rest, so the lookup does not fail. Which check wins
  depends on the row order, and that order changes as rows are updated.
- Mail sent to the victim's address can be recorded on the attacker's check. The attacker then
  reads its sender, subject and message, and the auth and SMTP details in the result output.
- The victim's check misses that mail and can open a false "down" incident.
- Someone who knows the token could already forge an "up" result by mailing the address. Reading
  the victim's real mail, and making their check miss it, are new.

## Proposal

1. Make the token server-managed. `checkemail/config/validate.go` and the checks service must
   ignore a `token` sent by a caller on create, on `PATCH`/`PUT` and on upsert. On create the
   server always mints one. On update it keeps the stored one (as it already does when the key is
   absent). The only way to change it is a dedicated rotate endpoint, like
   `RotateHeartbeatToken` (`service.go:2575`), if one is wanted.
2. Config-as-code import/apply: keep the existing check's token (`preserveAbsentRedactedFields`)
   and never take one from the document. Remove a token that the document carries for a new
   check, and mint a fresh one.
3. Defence in depth:
   - add a migration (next free `NNN`, never an edited one) with a partial unique index on the
     token for live email checks, in both dialects;
   - first, a step that re-mints the token of every duplicate but the oldest;
   - in `GetCheckByEmailToken`, treat more than one match as data corruption: log it and reject
     the mail, as the original spec says.
4. Keep the 48-hex format check in the inbound pattern (`emailcheck/handler.go:83`). Refuse a
   non-hex token on write if any code path still accepts one.

## Tests

- `server/internal/handlers/checks`: creating an email check with a caller-chosen `config.token`
  stores a different, server-minted token (regression: fails before the fix). A `PATCH` with
  `config.token` set to another check's token leaves the stored token unchanged (regression).
  Positive control: `TestPatchingEmailAuthOptionsKeepsTheToken` still passes, and a `PATCH`
  without `token` keeps it.
- Import/apply: a document with a token for a new email check gets a fresh token. The token of an
  existing check survives an apply.
- Both DB dialects: inserting two live email checks with the same token is refused by the new
  index. A deleted check's token does not block a new one.
- Migration test: a database with two live checks sharing a token migrates, the older keeps its
  token, and the other gets a new one.
- `emailcheck`: mail to a token that resolves to a single check is still recorded (positive
  control).

## To verify

- Whether the MCP check tools and the agent-facing APIs have their own path that writes
  `config.token`.
- Whether any client (the CLI, Terraform, the docs) documents setting a fixed token, for example
  to keep an address across a migration between instances. If so, the docs must say the token is
  now always minted by the server.
