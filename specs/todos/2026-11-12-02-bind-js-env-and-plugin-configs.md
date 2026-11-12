---
model: opus
effort: high
---

# Browser-sealed secrets of JS checks and plugin types are not bound to where they are sent

## Problem

Found by the security scan of 2026-11-12 (finding AK2).

**Severity: Medium.** This is a bypass of the target binding shipped in #840 (spec `2026-11-11-01`, finding AJ1), which no release contains yet. Who can trigger it:
- whoever can write a check's stored config: the operator, an attacker who compromised the server, or someone with write access to the database. This is the threat the binding exists for;
- an org writer, under the accepted trust model.

The binding puts a digest of the check type's *target fields* inside the sealed plaintext. The agent refuses the secrets when that digest no longer matches the job's public config (`server/internal/crypto/credentials/binding.go:181-217`, `checkworker/backend/ws_binding.go:23`). Two kinds of check types send their secrets somewhere their target fields do not cover:

1. **JS checks bind only `script`** (`server/internal/checkers/checkjs/config/config.go:399-401`).
   - The public `env` map (`config.go:61`) is exposed to the script as the read-only `env` global (`checkers/checkjs/checker.go:531-539`).
   - The docs present it as the place for "a base URL", next to `secrets` (`web/docs/docs/features/javascript-checks.md:78`). Every example builds its request URL from `env.BASE_URL` and sends `secrets.*` with it (`:259`, `:780-807`).
   - So changing `env.BASE_URL` sends the sealed secrets to another host, with an unchanged digest:
     - the server raises no `409 SEALED_TARGET_CHANGED`, because `TargetChanged` only compares `script`;
     - the agent accepts the blob.
   - The docs claim the opposite: "the script for JS checks", and "cannot point a sealed check at a host they control" (`web/docs/docs/features/browser-sealed-secrets.md:147,155`).
2. **Agent-reported plugin types bind only `DefaultTargetFields`.**
   - Neither `plugins.Config` nor `RemoteType` implements `TargetFielder` (`server/internal/plugins/config.go:45`, `remote.go:182`).
   - So `TargetFieldsFor` falls back to `url, host, hostname, port, database, dsn, address, endpoint, server, brokers` (`binding.go:63-65`, `checktypes/orgplugins.go:57`, `configregistry/binding.go:19-21`).
   - A nagios plugin's argument template can splice any config key into its command line (`plugins/manifest.go:78-81`). A plugin whose destination lives in another key (`target`, `uri`, `apiEndpoint`, …) can be retargeted without breaking the binding.

The built-in types with secrets all declare complete host/port/url lists. JS and plugins are different: their destination is decided by code from arbitrary config keys.

## Proposal

1. **JS checks.** Return `script` and `env` from `JSConfig.TargetFields()` (`checkjs/config/config.go:399`).
   - `env` is an object, and `writeCanonicalObject` already sorts its keys (`binding.go:399-427`).
   - dash0 computes the same digest from the `targetFields` list of `GET /check-types` (`web/dash0/src/lib/sealing.ts:162-181`). Add a JS case with an `env` map to the shared fixture `web/dash0/src/lib/__fixtures__/target-binding.json`, so Go and TS stay byte-identical.
   - Existing bound JS blobs get a digest mismatch once their `env` is non-empty. The Re-seal page must list them as *Seal again to bind to the target*, and the agent error must say "type them again". Check that a JS blob sealed with an empty `env` keeps its digest (absent fields are skipped).
2. **Plugin types.** Bind every public config key of a type that declares no target list, minus a small fixed list of keys that cannot steer a destination: `timeout`, `period`, `name` (To verify the exact keys).
   - Either add a `TargetFields()` to `plugins.Config`/`RemoteType` that returns "all keys", or give `TargetFieldsFor` an "all public keys" mode used when `cfg` is a plugin, or is nil (a type unknown to the agent).
   - Optionally let a manifest narrow the list with a schema keyword such as `x-solidping-target-fields`. Narrowing must never be the default.
   - `GET /check-types` must publish the same list, so dash0 and the agent agree.
3. **Server-side 409.** `assertSealedTargetKept` (`server/internal/handlers/checks/service.go:5659`) and the plan/apply/bulk paths use the same lists, so an `env` or plugin-key edit without new `sealedSecrets` answers `409 SEALED_TARGET_CHANGED`.
4. **Docs.** Update `browser-sealed-secrets.md:147` ("the script and its `env` for JS checks, every public key for plugin types") and the JS checks page. Add a `CHANGELOG.md` entry.

## Tests

- **Regression** in `server/internal/crypto/credentials/binding_test.go` or `checkworker/backend/ws_binding_test.go`, for both cases below (both fail on main today):
  - a JS blob bound to `{script, env:{BASE_URL: https://api.acme.com}}`, then claimed with `env.BASE_URL` changed to `https://evilco.example`, is refused with `ErrSealedForAnotherTarget`;
  - a plugin-type blob claimed after a non-default key (`target`) changes is refused the same way.
- **Positive controls:**
  - the same JS blob with only `timeout` changed is accepted;
  - a plugin blob with only `timeout` changed is accepted.
- In `handlers/checks/browser_sealing_target_test.go`, a PATCH changing a sealed JS check's `env` without new `sealedSecrets` answers 409. A PATCH changing its `timeout` answers 200.
- The shared fixture gains the JS-with-env vector, and both the Go fixture test and `web/dash0/src/lib/sealing.test.ts` pass on it.

## To verify

- Which public keys a plugin config can carry that are never a destination, for the exclusion list in step 2. When in doubt, bind the key: an over-broad binding only forces a re-type.
- Whether a JS check can read any other public config besides `env` (for example `ai`), and whether that should be bound too.
