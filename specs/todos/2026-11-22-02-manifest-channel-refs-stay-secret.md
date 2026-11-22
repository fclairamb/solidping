---
model: opus
effort: medium
---

# Manifest apply prints and stores the resolved value of a `${param:}` / `${env:}` reference in a non-secret channel setting

## Problem
Found by the security scan of 2026-11-22 (finding AU2). Severity: Low. Who can trigger it: an org admin, the role of `POST /orgs/:org/apply` (`server/internal/app/server.go:1304-1314`). On SaaS that is anyone, in their own org.

An org parameter saved with `secret: true` is write-only: its value never comes back from any route (`server/internal/handlers/orgparams/service.go:14-15`). An allowlisted `${env:}` variable is meant to be usable, not readable. The checks apply path keeps that promise: it stores the reference text, and its plan masks every value a reference produced (`maskReferences`, `server/internal/handlers/checks/diff.go:553-570`).

The new multi-kind apply (a20f9513, spec 2026-11-21-01) breaks it for channels:
- `planChannels` resolves every channel setting with `secretref.ResolveConfig` before diffing (`server/internal/handlers/manifestapply/channels.go:66`). The comment at `:21-25` says the plan never prints a resolved value.
- `diffChannel` masks only the keys in `credentials.ConnectionSecretFields(existing.Type)`. Any other key gets its plaintext in `From`/`To` (`channels.go:186-196`, `renderValue` at `:215`).
  - So a dry run with a reference under any non-declared key returns the decrypted parameter in the HTTP response.
  - Example: an extra key, or `webhook_url` of a type whose URL is stored in clear (`server/internal/crypto/credentials/conn_secrets.go:23`).
- The resolved settings are then written through `integrations.CreateIntegration` / `UpdateIntegration`. Only declared secret keys move to the encrypted private column, so the value stays in the public `settings` column.
  - `GET /orgs/:org/integrations` returns it (`server/internal/handlers/integrations/service.go:315-330`).
  - So does `GET /orgs/:org/export` (`manifestapply/export.go:77-89`), in a document stamped `secrets: stripped` that is meant to be committed to git.
- `diffChannel` renders `current[key]` from `EffectiveSettings` (decrypted, `integrations/service.go:2259-2265`). A key that holds a value in `settings_private` but is no longer in the registry would be printed too.

Impact is bounded: the caller is an admin of the org that owns the parameter, and a `user` can already send a reference's value to a server of their own through a check. The gain is reading back, in clear and without any outbound request, a secret a colleague, an operator or automation set. It also lands that secret in exported manifests.

## Proposal
1. `manifestapply/channels.go`:
   - Have `planChannels` record which settings keys came from (or contain) a `${param:}` / `${env:}` reference, before resolving them.
   - In `diffChannel`, mask those keys as well as `ConnectionSecretFields(type)` and any key present in the row's stored private settings.
   - Reuse the checks differ's rule: `${var:}` stays visible.
2. Refuse a `${param:}` / `${env:}` reference under a settings key the channel type does not declare secret. Fail the apply with an error naming the keys that do take secrets (e.g. `auth_token`, `access_token`). That way a resolved value can only end up in the encrypted column. Apply the same check to `POST /orgs/:org/checks/apply` if it routes channels.
3. `manifestapply/export.go`: no change needed once 2 holds. Add a comment that a non-secret key can never carry a resolved reference.

## Tests
- `server/internal/handlers/manifestapply/service_test.go`:
  - A channel with a reference to a secret org parameter under a non-declared key: a dry run must not contain the value in any `Change.From`/`To`, and a real apply is refused (2).
  - Both fail before the fix: the dry run prints the value, and the apply stores it in the public `settings`.
- Same file, positive control: the existing `TestApplyChannelSecretReferences` (reference under the declared `auth_token`) still applies, still stores the value only in `SettingsPrivate`, and its rotated-value dry run still prints neither value.
- Same file: a plain literal in a non-secret key still shows in clear in the plan diff, so the masking does not hide ordinary changes.
- Export: after an apply with a declared-secret reference, `GET /export` contains neither the value nor the key.

## To verify
- Whether any shipped channel type legitimately needs a secret in a key that `ConnectionSecretFields` does not declare (e.g. a token embedded in a URL). If one does, declare that key in `conn_secrets.go` rather than allowing references in public keys.
