---
model: sonnet
effort: medium
---

# Tunneled checks always fail when encryption is disabled: the SSH tunnel resolver can't open v3 plaintext envelopes

## Problem

On a server running without `SP_ENCRYPTION_MASTER_KEY`, every check that
references an SSH tunnel fails on every execution with:

```
tunnel failed: credentials: ssh tunnel: cannot decrypt the ssh check credentials: <uid> (encryption disabled)
```

Live example on the dev instance: HTTP check
`4d7dda60-6af0-47dd-bf3e-cf4a996e0495` ("RabbitMQ", tunneled through SSH check
`a347e725-2f89-4069-bc32-ba275df3d681`) is permanently down with the error
above, while the referenced SSH check itself is up.

This is fallout from spec `2026-07-18-06-check-secrets-never-exposed-api`:
since that fix, secret fields are **always** split out of the public `config`
column into `config_private` — with no master key, as a **v3 plaintext
envelope** (`{"v":3,"alg":"plaintext","data":{...}}`,
[plaintext_envelope.go](server/internal/crypto/credentials/plaintext_envelope.go)).
The envelope is openable without any key, and the central decrypt seam
`DecryptForOrg`
([service.go:232](server/internal/crypto/credentials/service.go:232)) opens it
*before* its enabled-gate precisely so callers need no special-casing.

Two callers still pre-gate on `creds.Enabled()` **before** reaching that seam,
so a plaintext envelope — the only format a no-master-key server ever writes —
is unreachable:

1. `sshtunnel.effectiveConfig`
   ([sshtunnel.go:262-279](server/internal/integrations/sshtunnel/sshtunnel.go:262)):
   sees a non-empty `ConfigPrivate`, hits `creds == nil || !creds.Enabled()`
   and returns `ErrSecretsUnavailable` with "(encryption disabled)". Its
   doc comment still describes the pre-2026-07-18-06 world ("the secrets were
   stored in plaintext on the public map"), as does the test
   `TestLoadConfigPlaintextFallback`
   ([sshtunnel_test.go:221](server/internal/integrations/sshtunnel/sshtunnel_test.go:221)).
2. Freebox `resolveAppToken`
   ([lanlookup.go:99-124](server/internal/integrations/freebox/lanlookup.go:99)):
   same stale pre-gate (`!creds.Enabled()` → "credentials disabled") and same
   stale "plaintext fallback on the public Settings map" comment, so LAN
   lookups through a Freebox channel break identically when encryption is
   disabled.

The rest of the codebase already handles this correctly, in one of two ways:

- `checkjobsvc.MergeJobSecrets`
  ([secrets.go:77-88](server/internal/checkworker/checkjobsvc/secrets.go:77))
  opens `IsPlaintextEnvelope` blobs first, then gates on `Enabled()` — which is
  exactly why the SSH check itself keeps passing while its dependents fail.
- API-side loaders gate with `credentials.RequiresKey(envelope) &&
  !creds.Enabled()` (e.g.
  [checks/service.go:3595](server/internal/handlers/checks/service.go:3595),
  [integrations/service.go:591](server/internal/handlers/integrations/service.go:591),
  [kubernetes/client.go:149](server/internal/integrations/kubernetes/client.go:149),
  [job_notification.go:102](server/internal/jobs/jobtypes/job_notification.go:102)),
  using the `RequiresKey` helper
  ([plaintext_envelope.go:91](server/internal/crypto/credentials/plaintext_envelope.go:91))
  written for exactly this gate.

## Proposal

**End goal: an HTTP check that itself carries credentials (e.g. `basicAuth`),
routed through an SSH tunnel check that also has to use credentials
(`private_key`/`password`), must work end-to-end** — in every storage mode,
including no-master-key. That is exactly the live example above: the HTTP
check's own secrets (`configPrivateKeys: ["basicAuth"]`) already reach the
checker via the claim-path merge; the tunnel's secrets must reach the resolver
the same way. Both credential layers must decrypt/open for a single execution
to succeed.

Bring the two stragglers onto the same rule: **a v3 plaintext envelope opens
without a key; only key-requiring envelopes may produce an
"encryption disabled" error.**

1. **`sshtunnel.effectiveConfig`** — mirror `MergeJobSecrets`' structure: after
   the empty-`ConfigPrivate` early return, if
   `credentials.IsPlaintextEnvelope(*check.ConfigPrivate)`, open it with
   `credentials.OpenPlaintext` and merge via `credentials.MergeConfig`
   (wrapping any open error in `ErrSecretsUnavailable`); only then fall through
   to the existing `creds == nil || !creds.Enabled()` gate for key-requiring
   envelopes. The direct-open form (rather than the `RequiresKey` gate) is
   deliberate: `creds` can legitimately be nil here and a plaintext envelope
   must still open. Refresh the stale doc comment.

2. **Freebox `resolveAppToken`** — same change: open a plaintext
   `SettingsPrivate` envelope before the `Enabled()` gate, keep the
   key-requiring error path, refresh the stale comment. (The trailing
   public-`Settings` fallback can stay for genuinely legacy rows.)

3. **Tests** —
   - `sshtunnel_test.go`: add a `LoadConfig` success case where
     `ConfigPrivate` is a real `credentials.SealPlaintext` envelope and creds
     is `&stubCreds{enabled: false}` (and one with a nil-ish/disabled decryptor
     to lock in the nil-safety); keep the
     "encryption disabled" failure case in `TestLoadConfigDecryptFailures` but
     make its envelope explicitly a non-plaintext blob so it still asserts the
     key-requiring path; update `TestLoadConfigPlaintextFallback`'s stale
     comment (public-map configs with no envelope must keep working).
   - Freebox: mirror coverage for `resolveAppToken` with a plaintext
     `SettingsPrivate` envelope on a disabled service.

No migration or data change: existing envelopes are already openable. The dev
"RabbitMQ" check should recover on its next cycle once the fix is deployed —
verify the end goal end-to-end against the running dev server: the tunneled
HTTP check goes up with BOTH credential layers in play (its own `basicAuth`
merged on the claim path AND the SSH check's `private_key` opened by the
resolver), and the result carries `tunnel_setup_ms` with no `tunnel_failed`.

## Implementation Plan

1. **`server/internal/integrations/sshtunnel/sshtunnel.go`** —
   `effectiveConfig`: after the empty-`ConfigPrivate` early return, branch on
   `credentials.IsPlaintextEnvelope(*check.ConfigPrivate)`. If true, open with
   `credentials.OpenPlaintext`, wrap any open error in `ErrSecretsUnavailable`,
   and merge via `credentials.MergeConfig` — no touch of `creds` at all (it can
   legitimately be nil here, mirroring `MergeJobSecrets`' structure). Otherwise
   fall through to the existing `creds == nil || !creds.Enabled()` gate and the
   `creds.DecryptForOrg` path unchanged. Refresh the stale doc comment above the
   function (currently describes the pre-2026-07-18-06 "plaintext on the public
   map" world).

2. **`server/internal/integrations/freebox/lanlookup.go`** —
   `resolveAppToken`: same shape as a 3-way switch (mirroring
   `MergeJobSecrets`): plaintext envelope → `OpenPlaintext` unconditionally;
   `creds == nil || !creds.Enabled()` → key-requiring "credentials disabled"
   error; else → `creds.DecryptForOrg`. Keep the trailing public-`Settings`
   fallback for rows with no private envelope at all. Refresh the stale doc
   comment.

3. **Tests**
   - `sshtunnel_test.go`:
     - Add `TestLoadConfigOpensPlaintextEnvelopeWithoutKey` (or similar): a
       real `credentials.SealPlaintext` envelope on `ConfigPrivate`, creds
       `&stubCreds{enabled: false}`, assert `LoadConfig` succeeds and the
       plaintext secret lands in the resolved config.
     - Add `TestLoadConfigOpensPlaintextEnvelopeWithNilCreds` (nil-safety lock
       in): same plaintext envelope, `creds = nil`, assert success.
     - `TestLoadConfigDecryptFailures`: change the envelope fixture from the
       ad hoc string `"envelope-blob"` to an explicit non-plaintext envelope
       (e.g. a `credentials.EncryptForOrg`-produced AES envelope, or a hand-
       built JSON blob with a different `v`/`alg`) so the case unambiguously
       exercises the key-requiring path rather than incidentally failing
       `IsPlaintextEnvelope` due to invalid JSON.
     - `TestLoadConfigPlaintextFallback`: update the stale doc comment (it
       still needs to cover the no-envelope, secrets-on-the-public-map case —
       that keeps working unchanged).
   - `lanlookup_test.go` (freebox): add a test that builds a granted channel
     with `SettingsPrivate` set to a `credentials.SealPlaintext` envelope
     carrying `appToken`, using a disabled credentials service (mirror
     `newLanlookupFixture`), and asserts `ListLanHostsForChannel` succeeds
     (exercises `resolveAppToken`'s new branch through the public entry
     point, since `resolveAppToken` itself is unexported).

4. Run `make fmt`, then `make build-backend lint-back test` until green.

5. Self-review against the spec's Proposal items 1-3 and the end-to-end
   acceptance description before reporting.
