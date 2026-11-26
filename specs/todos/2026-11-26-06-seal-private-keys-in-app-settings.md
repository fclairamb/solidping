---
model: opus
effort: medium
---

# Seal the SAML SP and VAPID private keys stored in `app_settings`

## Problem

Found by the security scan of 2026-11-26 (finding AY2).

- **Severity:** Low.
- **Who can trigger it:** anyone with read access to the database (a dump, a backup, a replica credential) on an instance that configured `SP_ENCRYPTION_MASTER_KEY`. No application account is needed.

#1033 (c72df380) seals the secret rows of `parameters` with the system DEK (`server/internal/db/sysparamseal/sysparamseal.go`, `server/internal/crypto/credentials/system_sealer.go`). Two private keys live in another table, `app_settings`, that nothing seals:

- The SAML SP private key `saml.sp_private_key_pem` (`server/internal/handlers/auth/saml_service.go:60`), read at `:188` and written at `:214` through `GetAppSetting` / `SetAppSetting`. It is generated on first use and stored as a plaintext PEM.
- The Web Push VAPID private key `webpush.vapid_private_key` (`server/internal/webpush/vapid.go:18`), written at `:105` and read at `:118` through `SetAppSettings` / `GetAppSettings`, unless `SP_WEBPUSH_VAPID_PRIVATE_KEY` provides it.

Impact:

- With the VAPID private key and the stored push subscriptions, anyone can sign valid VAPID requests and push arbitrary notifications, under SolidPing's identity, to the users who enabled Web Push.
- With the SP private key, anyone who also obtains an assertion the IdP encrypted to the SP certificate can decrypt it.

`web/docs/docs/configuration/security.md:39` says a dump of a database encrypted with the master key "does not reveal these secrets". These two keys are not covered.

## Proposal

1. Seal both values with the system sealer when a master key is set, the way `sysparamseal` seals the secret parameters. Either:
   - (recommended) move them to system parameters flagged `secret=true`, read from the old `app_settings` row once and deleted from there; or
   - wrap `GetAppSetting(s)` / `SetAppSetting(s)` for a fixed list of secret keys (`saml.sp_private_key_pem`, `webpush.vapid_private_key`) with the same `$sp_sealed` envelope.
2. Seal the existing plaintext rows at startup, next to `SealPlaintextRows` (`server/internal/app/server.go`, the `SP_ENCRYPTION_AUTO_MIGRATE` path). The key values do not change: existing push subscriptions and the IdP's copy of the SP certificate keep working.
3. A sealed value that cannot be opened fails like a sealed parameter (`ErrSealedUnavailable`). It must never be read as missing, or a new key would be generated over it: that would silently break every push subscription and the IdP's SP certificate.
4. Keep the SP certificate and the VAPID public key in plaintext. List both private keys in `security.md` next to the other encrypted server settings.

## Tests

- **Regression**: with a master key, after the SAML SP key pair and the VAPID key pair are created, the raw database row of each private key holds no PEM / base64 key material (read through the undecorated service). It fails before the fix.
- Migration: a database seeded with the two plaintext rows is sealed at startup, and the keys read back afterwards are byte-identical to the originals (VAPID public key unchanged, SP certificate unchanged).
- Failure path: a sealed row with a wrong master key makes the read fail, and no new key pair is generated.
- **Positive control**: without a master key, both keys still round-trip in plaintext as today, and Web Push sends plus SAML metadata keep working.
