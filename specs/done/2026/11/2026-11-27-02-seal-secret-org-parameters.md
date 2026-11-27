---
model: opus     # credential sealing at rest, plus a migration of existing rows
effort: high
---

# Secret org parameters (`${param:…}` values) are stored in plaintext despite the master key

## Problem

Found by the security scan of 2026-11-27 (finding AZ1). Severity: Medium. Who can exploit it: anyone
who can read the database (a dump, a backup, a read replica). No SolidPing account is needed.

Org admins keep their organization-managed secrets in org parameters. The API makes them
write-only by default (`server/internal/handlers/orgparams/service.go:152-155`: `secret` defaults to
`true`, `project` at `:212` never returns the value). Checks reference them as `${param:KEY}`, and
`secretref/envallow.go` points operators there for any secret they manage themselves.

The value itself is stored in clear:

- `SetOrgParameter` writes `value` as-is into `parameters.value`
  (`server/internal/db/postgres/postgres.go:5489-5520`, same in `server/internal/db/sqlite/sqlite.go`).
- `LookupParam` reads it back as-is (`server/internal/secretref/resolvers.go:59-76`). Its comment says
  so: "The `secret` flag only masks API responses, so the plaintext value is read directly here."
- `db/sysparamseal` wraps only the system rows (`GetSystemParameter`, `SetSystemParameter`, …:
  `server/internal/db/sysparamseal/sysparamseal.go:92-170`) and the two private keys in `app_settings`
  (`appsettings.go`). Org-scoped rows are never sealed.

With `SP_ENCRYPTION_MASTER_KEY` set, every other secret is sealed at rest: check config secrets,
integration credentials, the secret system parameters (#1033) and the SAML/VAPID private keys
(#1061). `web/docs/docs/configuration/security.md` ("What the envelope covers") says that a dump of
such a database "does not reveal these secrets". So the safest-looking place for an org's API keys is
the one place a dump reads them in clear, for every org at once. A secret typed inline in a check is
encrypted. The same secret moved into a `${param:…}` for reuse is not.

## Proposal

1. Seal the value of every org parameter row with `secret = true` in the org-managed namespace
   (`paramkeys.OrgKeyPrefix`, `usr.`, `server/internal/paramkeys/paramkeys.go:67`). Do it at the
   `db.Service` decorator level, so every reader gets the same treatment: `orgparams`, `secretref.LookupParam`,
   `checkjobsvc.ParamOverlay` (`server/internal/checkworker/checkjobsvc/paramrefs.go:33`) and any
   export. Platform rows (`encryption.dek`, region keys, `custom_regions`, …) are left alone. Their
   own sealing, or their lack of secrets, is out of scope.
   - Extend `sysparamseal.Service` with `GetOrgParameter`, `SetOrgParameter` and the org list method
     the store exposes. Reuse the envelope marker and the fail-closed rules the system rows already
     use: a sealed row that cannot be opened is `ErrSealedUnavailable` and is never read as missing or
     as an empty value. Without a master key, it is a no-op.
   - A non-secret row (`secret = false`) stays in plaintext: its value is returned by the API anyway.
   - A row that changes from secret to non-secret is rewritten in clear, and the other way round
     sealed, on the same `SetOrgParameter` call.
2. Startup migration: when `SP_ENCRYPTION_AUTO_MIGRATE` is on (default), seal the existing plaintext
   `secret = true` rows of the `usr.` namespace. Use the same pattern as `SealPlaintextRows` /
   `SealPlaintextAppSettings`: idempotent, already-sealed rows skipped, a failure fails the boot. The
   resolved values do not change, so every check keeps working.
3. Without a master key, add the count of plaintext secret org parameters to the startup warning
   that already lists the plaintext secret system settings (`warnPlaintextSystemSecrets`).
4. Docs: add "secret organization parameters (`${param:…}` values)" to the list in
   `web/docs/docs/configuration/security.md` ("What the envelope covers"), and add a CHANGELOG line.

## Tests

- `server/internal/db/sysparamseal`: with a master key, `SetOrgParameter(org, "usr.x", "s3cret", true)`
  stores a row whose raw `value` (read through `Raw()`) does not contain `s3cret` and is an envelope.
  `GetOrgParameter` returns `s3cret`. Fails before the fix (the raw row holds the value).
- Same file, positive control: `secret = false` stays in plaintext in the raw row, and a platform key
  outside `usr.` (for example `custom_regions`) is untouched.
- A sealed row read with a different master key returns `ErrSealedUnavailable`, never `nil` or `""`.
- `secretref.LookupParam` through the decorated service resolves a sealed parameter to its clear value,
  so a check that uses it still runs. Covered on SQLite (`make test`) and on Postgres.
- Migration: a database with plaintext secret org parameters is sealed at startup, a second boot
  seals nothing (`0` rows), and the values resolve unchanged.
- `orgparams` API: List/Get still elide secret values and still return non-secret values.

## Open questions

- Seal with the system DEK (the `sysparamseal` decorator as is) or with the org's own DEK, the way
  check configs are sealed? **Recommended: the system DEK**, through the existing decorator. That
  keeps one wrapper, one fail-closed path and one startup sweep, and avoids a lookup of the org DEK
  row (itself in `parameters`) from inside the decorator that reads `parameters`. Org-DEK separation
  only matters against an attacker who already holds the KEK.

## Resolved open questions

- Seal secret org parameters with the **system DEK**, through the existing `sysparamseal` decorator
  (one wrapper, one fail-closed path, one startup sweep; no org-DEK lookup from inside the decorator).
  (autopilot: recommended answer)

## To verify

- Every production path that reads org parameters goes through the decorated service (as
  `app/server.go` does for system rows), including `credentials/param_store.go:50-80`, the sealing
  store (`server/internal/sealing/sealing.go:69-70`) and the MCP tools.
- Any manifest or export path that reads `usr.` rows (it must keep eliding secret values).
