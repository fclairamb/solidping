---
model: opus
effort: low
---

# Treat the ntfy legacy `accessToken` setting as a secret

## Problem

Found by the security scan of 2026-11-25 (finding AX2).

- **Severity:** Low.
- **Who can trigger it:** any org member with read access, the viewer role included. It needs an org whose ntfy integration holds its token under the legacy `accessToken` key.
- **How a token ends up under that key:**
  - From 2026-03-29 (eea049e5) to 2026-10-09 (03be40ca), the ntfy sender read only `accessToken`. Tokens set through the API or config-as-code in that period sit under that key.
  - A client that still writes the legacy name today, through the API, Terraform or Ansible, also stores it there.

The sender still uses the legacy key as a live bearer credential:

- `server/internal/notifications/ntfy.go:63-70` falls back to `settings.AccessToken` and sends it as `Authorization: Bearer`.
- `ntfySettings.AccessToken` (`ntfy.go:103`) is the `accessToken` JSON key.

But the credentials registry declares only the canonical key: `ConnectionTypeNtfy: {secretKeyAuthToken}` (`server/internal/crypto/credentials/conn_secrets.go:33`). So `accessToken`:

- stays in the public `settings` JSONB, never encrypted. `credmigrate.ReconcileConnectionRegistry` (`server/internal/credmigrate/reconcile.go:50`) only re-splits the declared keys;
- is returned verbatim by `toResponse` (`server/internal/handlers/integrations/service.go:296-346`, `includeSettings=true` on the single GET at `:424`), which redacts only the declared keys. Any org member can therefore read the token. That is the N5 exposure class;
- appears in config-as-code exports.

A member who reads it can publish to the org's ntfy topic, or to any topic the token allows on that ntfy server, outside SolidPing and its audit trail.

## Proposal

1. Add `"accessToken"` to `connectionSecretFields[models.ConnectionTypeNtfy]` (`conn_secrets.go:33`).
   - Redaction follows: `toResponse` redacts and advertises it.
   - So does encryption: the storage split and the startup reconcile move existing rows into `settings_private`.
   - The sender's merged settings still carry it, so delivery keeps working.
2. Optional, recommended: on create and PATCH of an ntfy integration, rename a supplied `accessToken` to `auth_token` when `auth_token` is absent. Then new rows only ever use the canonical key. Keep the sender's read fallback for the rows not migrated yet.

## Tests

- `handlers/integrations`: create an ntfy integration with `settings.accessToken` set, then GET it as a viewer. `settings` has no `accessToken`, and `settingsPrivateKeys` lists it. This fails before the fix.
- `credmigrate`: a row with `accessToken` in the public settings is split into `settings_private` by the reconcile, and the public JSON no longer holds the value.
- `notifications/ntfy_test.go`: after the merge, a connection whose token came from `accessToken` still sends `Authorization: Bearer <token>`.
- Positive control: a non-secret ntfy setting (`server_url`, `topic`) is still returned in `settings`.
