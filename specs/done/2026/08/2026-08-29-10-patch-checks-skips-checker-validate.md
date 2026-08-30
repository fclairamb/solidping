---
model: sonnet
effort: high
---

# PATCH /checks never invokes checker.Validate, so invalid per-type config is accepted and only fails at execution time

## Problem

Sending `"expectedStatusCodes": [200, 403]` (JSON ints instead of strings) on an
HTTP check is correctly rejected with a 400 on **create**, but silently
**accepted and persisted** on **update** (PATCH). Reproduced against the real
service (in-memory SQLite, config decoded from JSON so ints arrive as JSON
numbers):

- `CreateCheck` → `expectedStatusCodes: element 0 must be a string` (400). ✓
- `UpdateCheck` → no error; the row now stores `expectedStatusCodes: [200, 403]`
  as numbers. ✗

The type check itself exists and works: `HTTPConfig.FromMap` rejects non-string
array elements (`server/internal/checkers/checkhttp/config.go:186`), and both
the create path (`server/internal/handlers/checks/service.go:1330`) and the
dry-run validate endpoint (`server/internal/handlers/checks/validate.go:401`)
run it via `checker.Validate`.

The PATCH path is the hole: `UpdateCheck` → `applyConfigUpdate`
(`service.go:4199`) runs only

1. `normalizeCheckConfig` — for HTTP that is `NormalizeConfig`, which folds
   `username`/`password` into `basicAuth` and touches nothing else; and
2. the shared validators in `configValidationErrors` (`validate.go:285`):
   timeout cap, IP-version rule, tunnel references, SMTP send-mode.

`checker.Validate` is never called — the gap is even self-documented in the
comment at `service.go:4217` ("UpdateCheck never calls checker.Validate").

**Consequence:** the malformed config persists and every subsequent run fails at
the worker's parse step (`server/internal/checkworker/worker.go:938`,
`ErrFailedToParseConf`), so the user gets a check permanently in error state
instead of a 400 at write time. This applies to *every* per-type rule, not just
`expectedStatusCodes` — a PATCH can also smuggle in an invalid method, a
non-string header value, an empty/`ftp://` URL, etc.

Why the gap presumably exists: `checker.Validate` mutates its spec (HTTP
auto-generates name/slug from the URL; heartbeat/email generate a token when
absent), which is create-shaped behavior the PATCH path didn't want — so the
call was skipped wholesale rather than just its mutations.

## Proposal

In `applyConfigUpdate` (`service.go:4199`), after the preserve-absent-secrets
merge and normalization but before `applyEncryption`, run the checker's own
`Validate` against the **merged effective config**, mirroring what create and
the dry-run endpoint already enforce:

- Look up the checker via `registry.GetChecker(checkerdef.CheckType(check.Type))`;
  unknown type → keep today's pass-through behavior.
- Hand `Validate` a **deep copy** of the merged config, exactly the way the
  offline document validator does
  (`server/internal/handlers/checks/validate_document.go:204`,
  `deepCopyConfig`) — some checkers mutate the map (heartbeat/email token
  autogeneration), and nothing a checker writes may leak into the stored row.
  Use a throwaway `CheckSpec` (empty Name/Slug are fine; HTTP's name/slug
  autogeneration then writes into the throwaway spec, not the check).
- A returned `*checkerdef.ConfigError` must surface as a 400 — the handler
  translation already exists (`handler.go` routes `checkerdef.IsConfigError`
  through the validation-error path), so returning the error from
  `applyConfigUpdate` should be enough; verify with a handler-level test.

### The sealed-only trap (must be handled, not discovered in review)

For a **sealed-only** check (private-region credential sealing,
`ConfigSealed != nil`, `ConfigPrivate == nil`), `loadDecryptedConfig`
(`service.go:4428`) cannot read the secrets, so the merged effective config
**lacks the secret values**. Several checkers' `Validate` requires a secret —
e.g. SFTP demands `password` or `private_key`
(`server/internal/checkers/checksftp/config.go:134`). A naive
`checker.Validate(merged)` would therefore reject a perfectly valid, unrelated
PATCH (rename a path, bump a timeout) on such a check.

Handle it by re-injecting **string placeholders** into the deep-copied map for
every key named in `check.ConfigPrivateKeys` that is absent from the merged
config, before calling `Validate`. Secrets are strings, so placeholders satisfy
both type checks and non-empty "is required" rules. Verified safe today:
checkers parse secret material (e.g. `ssh.ParsePrivateKey`) at Execute, not
Validate — but the implementation must grep all `Validate` implementations for
secret-content parsing (beyond presence/type checks) and note the result; if
one is found, skip placeholder injection for that key and relax to
presence-optional there rather than shipping a PATCH that can't touch that
check type.

### Tests

- Regression: PATCH `"expectedStatusCodes": [200, 403]` (decoded from a real
  JSON string, so elements are `float64`) on an HTTP check → error mapping to
  400, nothing persisted. Positive control: the same PATCH with
  `["200", "403"]` succeeds.
- At least one other per-type rule enforced on PATCH (e.g. invalid `method`)
  to prove this isn't special-cased to one field.
- Sealed-only guard: an unrelated PATCH on a sealed-only check of a
  secret-requiring type (e.g. SFTP) still succeeds, and the sealed blob is
  preserved as before (existing spec 2026-07-16-02 contract).
- Plaintext/AES-envelope rows: PATCH still validates against the *full* merged
  config (secrets present), unchanged behavior otherwise.

### Non-goals / notes

- The stale comment at `service.go:4214-4218` must be updated — it currently
  documents the gap as intended behavior.
- Existing rows already carrying malformed config are out of scope (no data
  fixer here); they keep failing at the worker with `ErrFailedToParseConf`
  until re-PATCHed, which the new validation then catches.
- No frontend change: dash0 sends strings; this is an API-level hardening.

## Implementation Plan

1. **`server/internal/handlers/checks/service.go` — `applyConfigUpdate`** (around line 4199):
   after `normalizeCheckConfig` and before `firstConfigValidationError`/`applyEncryption`, call a
   new `s.validatePatchedConfig(check.Type, merged, injectKeys)` that:
   - Looks up `registry.GetChecker(checkerdef.CheckType(check.Type))`; unknown type → `nil` (pass
     through, unchanged behavior).
   - Deep-copies `merged` via the existing `deepCopyConfig` helper (already in
     `validate_document.go`, same package) so nothing a checker's `Validate` mutates (HTTP
     name/slug, heartbeat/email token) leaks into the stored row.
   - Calls `checker.Validate(&checkerdef.CheckSpec{Config: configCopy})` and returns its error
     verbatim (a `*checkerdef.ConfigError` already maps to 400 in the handler).
   - Update the stale comment at `service.go:4214-4218` — it currently says PATCH's only
     validation is normalization; that becomes false.

2. **Sealed-only placeholder injection**: capture `wasSealedOnly := check.ConfigSealed != nil &&
   check.ConfigPrivate == nil` at the top of `applyConfigUpdate` (before the merge touches
   anything), alongside the existing `oldSealed`/`oldPrivateKeys` captures. Parse `oldPrivateKeys`
   (JSON array string) into `[]string`. Only when `wasSealedOnly`, inject a placeholder into the
   deep-copied config for every key in that list that's absent from `merged` — never when the row
   is plaintext/AES-envelope, so an explicit secret-clearing PATCH still hits a real "is required"
   error instead of being silently masked.
   - Generic placeholder: a fixed non-empty string, satisfies presence + type checks for
     ordinary secret fields (password, token, apiKey, secretHeaders, basicAuth, …).
   - `private_key` is special-cased to a syntactically valid (but content-empty) PEM block —
     see finding below.

3. **Grep finding (spec explicitly requires this to be verified, not assumed)**: grepped every
   `internal/checkers/**/*.go` for `pem.Decode|x509.Parse|ssh.Parse|jwt.Parse|tls.X509KeyPair|
   ParsePrivateKey|ParseCertificate`. Two hits beyond the already-known Execute-path
   `ssh.ParsePrivateKey` calls (checkssh/checker.go, checksftp/checker.go, both in
   `executeWithAuth`, not Validate):
   - `checksftp/config.go:150` and `checkssh/config.go:199` both define an identical
     `validatePrivateKey(key string) error`, called from `SFTPConfig.Validate()` /
     `SSHConfig.Validate()` — which the checker's top-level `Validate(spec)` calls. It runs
     `pem.Decode` and, only if that fails (`block == nil`), returns
     `ConfigError{"private_key", "invalid PEM format"}`. If PEM-decode succeeds, every subsequent
     `x509.Parse{PKCS8,PKCS1,EC}PrivateKey` failure is swallowed ("Accept anyway — golang.org/x/
     crypto/ssh can parse OpenSSH format keys") and the function returns `nil` regardless of
     whether the bytes are a real key.
   - **Consequence**: a bare-string placeholder for `private_key` fails `pem.Decode` and would
     falsely reject an unrelated PATCH on a sealed-only SSH/SFTP check whose secret is a private
     key (not a password) — exactly the regression the spec warns against.
   - **Resolution**: for the `private_key` key specifically, inject a syntactically valid PEM
     block (`-----BEGIN PLACEHOLDER-----\n<base64 garbage>\n-----END PLACEHOLDER-----\n`) instead
     of the generic string placeholder. `pem.Decode` accepts any label, so this passes the shape
     check; the swallowed x509 failures mean the garbage body never causes a rejection. This is
     not "presence-optional" in the literal sense the spec's fallback text suggests (that would
     reopen the sealed-only regression for SFTP/SSH's private_key case), but it satisfies the same
     goal: no real secret content is asserted, nothing is persisted, and the checker's own
     leniency (existing behavior, unrelated to this change) is what makes it safe.
   - No other checker's `Validate` (all 39 types) parses secret content beyond `FromMap`'s
     type/presence checks — confirmed by the same grep across the entire `internal/checkers` tree,
     not just the `Validate(spec *checkerdef.CheckSpec)` signature (which would have missed the
     nested `config.go` helper).

4. **Tests** (`server/internal/handlers/checks/`, likely a new `patch_validate_test.go` or added to
   an existing PATCH test file):
   - Regression: PATCH a check's config with `expectedStatusCodes` decoded from real JSON
     (`json.Unmarshal` a literal `[200, 403]`, not `[]any{200, 403}`) → 400, config unchanged in
     the DB. Positive control: same PATCH with `["200", "403"]` → 200, persisted.
   - A second per-type rule on PATCH (e.g. invalid HTTP `method`) → 400.
   - Sealed-only guard: create an SFTP check with a private region + `password` secret so it goes
     sealed-only, then PATCH an unrelated field (e.g. `timeout`) → 200, sealed blob unchanged,
     password still absent from the public config.
   - Sealed-only + `private_key`: same shape but with an SFTP/SSH check whose secret is
     `private_key` → unrelated PATCH still succeeds (proves the PEM-placeholder path).
   - Plaintext/AES-envelope: PATCH an unrelated field on a check with a stored secret (non-sealed)
     → still 200, and a regression test that explicitly *clears* a required secret via PATCH still
     gets a real validation error (proves placeholders are never injected outside the sealed-only
     case).

5. **QA**: `go build ./internal/handlers/checks/...`, targeted `go test -run`, then
   `make build-backend lint-back test` once from repo root.
