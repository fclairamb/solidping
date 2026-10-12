---
model: opus
effort: medium
---

# Webhook signing secrets use standard base64, so the official Standard Webhooks libraries accept them as is

## Problem
Webhook notifications are signed with the Standard Webhooks scheme. The secret is shown in the dashboard and copied by the receiver into its verifier. The format SolidPing generates is not the one the official libraries read.

**Generation.** Two identical copies of `generateWebhookSecret` build `whsec_` + `base64.RawURLEncoding` of 32 random bytes, so URL-safe alphabet (`-`, `_`) and no padding:
- `server/internal/notifications/webhook.go:113-122` (used by `ensureSecrets` to auto-generate on first delivery, `:357-369`).
- `server/internal/handlers/integrations/service.go:103-112` (used on create through `ensureWebhookCreateSecret`, `:117-134`, called at `:435`, and by `RotateWebhookSecret`, `:1542-1567`).

The constants block and its comment (`service.go:91-101`, `webhook.go:31-34`) describe the format as `whsec_<base64url-unpadded-32-bytes>`.

**Signing.** `signRequest` (`webhook.go:127`) strips the prefix and decodes with `base64.RawURLEncoding.DecodeString` (`:141`). So signing accepts only the URL-safe unpadded form. A secret in standard base64 (contains `+`, `/` or a trailing `=`) fails with `ErrInvalidSigningSecret`, and every delivery of that channel fails.

**User-supplied secrets are not validated.** `ensureWebhookCreateSecret` keeps any non-empty `signingSecret` sent on create (`service.go:122-124`). `applyUpdateSettings` (`service.go:990`) merges a PATCHed `signingSecret` through `credentials.MergePatch` with no check. A pasted standard base64 secret, which is what Svix-style tools hand out, is stored and then breaks signing at the first delivery.

**What the official libraries do.** `github.com/standard-webhooks/standard-webhooks/libraries` v0.0.1:
- Go: `NewWebhook` decodes with `base64.StdEncoding` (`go/webhook.go:23,40`). It rejects any unpadded 43-character secret, so it rejects every secret SolidPing generates.
- Python: `base64.b64decode(whsecret + "==")` in non-validating mode (`python/standardwebhooks/webhooks.py:30`). It silently drops `-` and `_`, so it either errors or derives a different key.

**Measured on 2026-10-12** on 50 secrets from the real generator: the official Go library rejected 50/50, the Python library accepted 14/50 (about one secret in four has no `-` or `_` at all). After converting each secret to standard padded base64, both libraries verified 50/50.

**Docs.** PR #533 (commit `349904470`) documents the current format and a conversion workaround in `web/docs/docs/configuration/notifications.md`: step 2 of the signature description (`:990-992`), the Python snippet's `decode_secret` (`:1006-1011`), and the "Standard Webhooks libraries" tip that tells readers to convert the secret by hand (`:1031-1038`).

**UI.** `WebhookSigningPanel` (`web/dash0/src/components/integrations/integration-form.tsx:1147`) shows `settings.signingSecret` verbatim in a read-only input and copies it as is. It has no format logic. The E2E test only checks the prefix (`web/dash0/e2e/channels-webhook.spec.ts:55,90`).

Out of scope: status page subscriber webhooks (`X-SP-Signature`, `web/docs/docs/features/status-pages.md:813-828`). They use a different signing scheme and do not go through these functions.

## Proposal
1. **One home for the secret format, in `server/internal/notifications`.** The integrations service already imports that package (`service.go:37`). Add, in a new `server/internal/notifications/webhook_secret.go`:
   - `GenerateWebhookSecret() (string, error)`: `whsec_` + `base64.StdEncoding` of 32 bytes from `crypto/rand`. Always 50 characters, ending in a single `=`.
   - `DecodeWebhookSecret(secret string) ([]byte, error)`: strip an optional `whsec_` prefix, strip up to two trailing `=`, reject any other `=`, map `-` to `+` and `_` to `/`, then decode with `base64.RawStdEncoding`. Reject an empty key. Errors wrap `ErrInvalidSigningSecret`. This accepts standard or URL-safe, padded or not, so every secret ever stored still decodes to the same bytes.
   - `CanonicalWebhookSecret(secret string) (string, error)`: `whsec_` + `base64.StdEncoding` of `DecodeWebhookSecret(secret)`. Same key bytes, standard spelling. Idempotent.

   Delete both private `generateWebhookSecret` copies and the duplicated `webhookSecretPrefix` / `webhookSecretBytes` constants in `handlers/integrations/service.go`. Call the exported functions instead. Fix the comments that say "base64url-unpadded".
2. **Signing accepts both encodings.** `signRequest` (`webhook.go:138-144`) decodes with `DecodeWebhookSecret` instead of `RawURLEncoding`. Nothing else in the signature changes: the HMAC is over the same bytes, and the `v1,` value stays standard base64 (`:148`). A given secret produces exactly the same signature as before this change, whatever spelling is stored.
3. **New secrets are standard.** `ensureSecrets` auto-generation (`webhook.go:358`), `ensureWebhookCreateSecret` (`service.go:126`) and `RotateWebhookSecret` (`service.go:1555`) use `GenerateWebhookSecret`.
4. **User-supplied secrets are validated and stored canonical.**
   - On create, `ensureWebhookCreateSecret` runs a supplied `signingSecret` through `CanonicalWebhookSecret`. Undecodable input returns `ErrInvalidSettings` (400), not a channel that fails at first delivery.
   - On PATCH, `applyUpdateSettings` does the same for webhook channels when the request carries a non-empty `signingSecret`. An explicit null or empty value keeps today's meaning (cleared, then auto-generated on next delivery). Do not validate a stored value the request did not touch.
   - Do not add a length rule on the signing path: a legacy stored value of any length must keep signing. On input, see "To verify" for the 24 to 64 byte range.
5. **Existing secrets: re-encode at read time, not in storage.** Decision, with the reasoning:
   - Re-encoding never changes the key bytes. A receiver that already holds the old string keeps verifying: the signatures are identical byte for byte. Nothing breaks for anyone who does not re-copy.
   - The person who gains is whoever copies the secret from now on: the string they see works in the official libraries without conversion. It is also exactly the string the #533 workaround told people to produce, so anyone who followed the docs sees the value they already use.
   - The only person who can break is someone who wrote a strict URL-safe-only decoder from the old docs and then re-copies the secret. Our own documented snippet accepts both spellings. That risk is small and is named in the docs (step 7).
   - A storage sweep (a `credmigrate` pass like `NormalizePushoverSettings`, `server/internal/credmigrate/pushover.go:37`, wired from `maybeNormalizePushoverSettings`, `server/internal/app/server.go:4373`) would rewrite encrypted `settings_private` rows at boot for no change in behaviour, and would not run where `Encryption.AutoMigrate` is off. Read-time canonicalization covers every deployment with no data write.

   So: `injectSigningSecrets` (`service.go:365`) returns `CanonicalWebhookSecret(v)` for `signingSecret` and `signingSecretPrevious`. If a stored value does not decode, return it unchanged and log a warning with the channel UID (never the value). Stored values become canonical over time as channels are rotated or PATCHed. No migration, no SQL.
6. **Rotation and the 24 h grace window.** No change to the lifecycle (`webhookRotationGrace`, `service.go:97`; purge in `ensureSecrets`, `webhook.go:350-355`). After rotating a legacy channel, the active secret is standard and the previous one may be legacy URL-safe. `signRequest` decodes both with the tolerant decoder, so the header still carries two valid `v1,` entries during the window. The dashboard shows both in standard spelling (step 5).
7. **Docs** (`web/docs/docs/configuration/notifications.md`):
   - Step 2 (`:990-992`): the key is the secret without `whsec_`, base64-decoded. SolidPing generates 32 random bytes in standard base64 with padding.
   - The tip (`:1031-1038`): drop the conversion instructions. Say the official libraries take the secret as shown, prefix included.
   - Keep `decode_secret` in the Python snippet accepting both spellings (it costs nothing), and change its comment to say why: secrets created before this release were URL-safe without padding, and the dashboard now shows the same key in standard form.
   - Add one sentence under "Rotating the secret": a secret copied before this release is the same key in another spelling and keeps working; no rotation needed.
8. **UI.** No logic change: the panel shows what the API returns. Optionally tighten the help text (`form.signingSecretHelp` in `web/dash0/src/locales/{en,fr,de,es}/integrations.json:195`) to say the secret works as is with the Standard Webhooks libraries. If you change it, change all four locales.

## Tests
Go tests follow `server/AGENTS.md`: table-driven, `require.New(t)`, `t.Parallel()`.

**Interop dependency.** Use the official Go library as a test-only import: `github.com/standard-webhooks/standard-webhooks/libraries/go` (package `standardwebhooks`). It is already in the module graph at v0.0.1 as an indirect dependency of `anthropic-sdk-go` (`server/go.mod:170`), it is MIT licensed, and `depguard` only restricts non-test files (`server/.golangci.yml:104-115`). Importing it from a `_test.go` file drops the `// indirect` marker and downloads nothing new. Use `VerifyIgnoringTimestamp`, or a timestamp of `time.Now()`, since `Verify` enforces a 5 minute tolerance. If the dependency is refused at review, fall back to a ~20 line reference verifier in the test that decodes with `base64.StdEncoding` exactly like the library does.

New `server/internal/notifications/webhook_secret_test.go`:
- `TestGenerateWebhookSecret_Format`: 1 000 iterations. Each secret has the `whsec_` prefix, is 50 characters, ends in exactly one `=`, uses only `[A-Za-z0-9+/=]`, decodes with `base64.StdEncoding` to 32 bytes, and is unique across the run.
- `TestGenerateWebhookSecret_OfficialLibraryAccepts`: 500 iterations. `standardwebhooks.NewWebhook(secret)` succeeds, and it verifies a request signed by `signRequest([]string{secret}, ...)`. This is the regression guard for the bug.
- `TestDecodeWebhookSecret` (table). For one fixed key chosen to contain both `+` and `/` in standard form, these all decode to the same bytes: standard padded, standard unpadded, URL-safe padded, URL-safe unpadded, each with and without the `whsec_` prefix. Error cases wrap `ErrInvalidSigningSecret`: `""`, `"whsec_"`, `"whsec_not base64!!"`, `"whsec_a=bc"` (padding in the middle), `"whsec_abc==="` (three pads), a body whose length mod 4 is 1.
- `TestDecodeWebhookSecret_RandomEncodings`: property-style loop, 2 000 iterations, random key length 24 to 64 bytes. Encode each key four ways (`StdEncoding`, `RawStdEncoding`, `URLEncoding`, `RawURLEncoding`), with and without prefix. All decode to the original bytes. `signRequest` returns the identical header for all eight spellings. `standardwebhooks.NewWebhookRaw(key)` verifies that header. This loop is what catches the padding and `-`/`_` cases that a single vector misses.
- `TestCanonicalWebhookSecret` (table): a legacy URL-safe unpadded secret becomes standard padded with the same bytes. Canonicalizing twice equals canonicalizing once. The known vector `whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw` (32 characters, valid in both alphabets, no padding needed) is returned unchanged.
- `TestLegacySecret_OfficialLibraryRejects`: a `RawURLEncoding` secret containing `-` or `_` is rejected by `standardwebhooks.NewWebhook`, and its canonical form is accepted. Documents why step 5 exists.

`server/internal/notifications/webhook_test.go`:
- `TestSignRequest_KnownVector` (`:26`) passes unchanged. Add an assertion that `standardwebhooks.NewWebhook(secret).Sign(id, ts, body)` returns the same value.
- `TestSignRequest_InvalidSecret` (`:73`) passes unchanged.
- New `TestWebhookSender_Send_TwoActiveSecretsMixedEncodings`, modelled on `TestWebhookSender_Send_TwoActiveSecrets` (`:291`): active secret standard, previous secret legacy URL-safe with a valid future expiry. The header has two `v1,` entries; `NewWebhook(active)` verifies the request and `NewWebhookRaw(DecodeWebhookSecret(previous))` verifies it too.
- `TestWebhookSender_Send_AutoGeneratesSecret` (`:232`): assert the generated secret is in standard form (ends with `=`, decodes with `StdEncoding`).

`server/internal/handlers/integrations/webhook_secret_test.go`:
- `TestCreateWebhookChannel_GeneratesSecret` (`:66`) and `TestRotateWebhookSecret_CyclesSecrets` (`:95`): assert standard form, not just the prefix.
- New table test for a supplied secret on create: standard padded is stored as is; URL-safe unpadded is stored canonical; `"whsec_not base64!!"` returns 400 and creates nothing.
- New table test for PATCH: valid URL-safe value is stored canonical; garbage returns 400 and the stored secret is unchanged; explicit empty clears it as today; a PATCH that does not mention `signingSecret` (e.g. renaming) succeeds even if the stored value is legacy.
- New read-time test: a channel whose stored `signingSecret` and `signingSecretPrevious` are legacy URL-safe is returned by GET and by rotate in standard form, and the stored value is untouched by GET.

E2E `web/dash0/e2e/channels-webhook.spec.ts:55,90`: tighten `/^whsec_/` to `/^whsec_[A-Za-z0-9+/]{43}=$/`.

## To verify
- Whether the dashboard form ever sends `signingSecret` back on PATCH (search `integration-form.tsx` and `web/dash0/src/api/hooks.ts`). If it round-trips the displayed value, step 5 means a legacy row gets rewritten canonical on the next save. That is fine (same bytes), but confirm the PATCH test covers it.
- That `ErrInvalidSettings` is mapped to a 400 with a stable error code by the integrations handler on both create and PATCH. If not, add the mapping.
- Whether `signingSecretPrevious` can also be set through PATCH via `MergePatch`. It should be server-owned like the Teams bot fields (`restoreMSTeamsBotServerFields`, called at `service.go:1007`). If it is writable, decide in the PR whether to restore it from the stored settings; do not silently widen this spec.
- The Standard Webhooks spec's recommended secret size (24 to 64 bytes). If the spec states it, enforce it on input only (create and PATCH), never on the signing path. If it does not, enforce only "decodes to at least 1 byte".
- That `go mod tidy` keeps `github.com/standard-webhooks/standard-webhooks/libraries` at v0.0.1 and only moves it out of the `// indirect` block.
- Other readers of the secret format: the MCP tools and any API docs or OpenAPI description of the integration settings (`grep -rn signingSecret` outside `server/internal/notifications` and `handlers/integrations`).

## Acceptance Criteria
- [ ] New secrets (create, rotate, auto-generate) are `whsec_` + standard padded base64 of 32 bytes.
- [ ] The official Go library verifies a SolidPing delivery using the secret copied from the dashboard, with no conversion.
- [ ] Existing URL-safe secrets keep signing, and their signatures are unchanged byte for byte.
- [ ] GET and rotate return both secrets in standard form; stored values are not rewritten by a read.
- [ ] Invalid user-supplied secrets are rejected with a 400 on create and PATCH.
- [ ] One generator and one decoder, in `server/internal/notifications`; the duplicate in `handlers/integrations` is gone.
- [ ] The docs no longer tell readers to convert the secret.
