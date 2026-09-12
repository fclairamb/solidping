---
model: sonnet
effort: high
---

# The sealed-secret placeholder is always a string, so a map-shaped secret field fails validation

## Problem

`injectSecretPlaceholders` ([server/internal/handlers/checks/service.go:4758](server/internal/handlers/checks/service.go#L4758))
writes `placeholderSecretValue` — a plain string — for **every** key named in
`configPrivateKeys` that is absent from the config it is handed:

```go
for _, key := range keys {
    if _, present := config[key]; present { continue }
    if key == "private_key" { config[key] = placeholderPrivateKeyPEM; continue }
    config[key] = placeholderSecretValue
}
```

The single `private_key` special case shows the shape problem was already met
once and patched by name. It is not the only key whose shape is not a string:
three declared secret fields are `map[string]string`, and each checker's
`FromMap` rejects a string outright.

| Check type | Secret key | `FromMap` rejection |
|---|---|---|
| `checkhttp` | `secretHeaders` ([config.go:295](server/internal/checkers/checkhttp/config.go#L295)) | `secretHeaders: must be a map[string]string` |
| `checkgrpc` | `secretMetadata` ([config.go:132](server/internal/checkers/checkgrpc/config.go#L132)) | `secretMetadata: must be a map[string]string` |
| `checkjs` | `secrets` ([config.go:63](server/internal/checkers/checkjs/config.go#L63), spec 2026-09-11-05) | `secrets: must be a map of string key-value pairs` |

Two call sites inject, and **both** are affected:

1. **PATCH on a sealed-only check** — `validatePatchedConfig`
   ([service.go:4731](server/internal/handlers/checks/service.go#L4731)) injects
   when `wasSealedOnly` is true (the check targets private regions only, so the
   server cannot decrypt its secrets and `merged` cannot carry them). An
   *unrelated* PATCH — a rename, a timeout bump — on such a check carrying
   `secretHeaders` / `secretMetadata` / `secrets` is rejected with a spurious
   `VALIDATION_ERROR` naming a field the request never touched.

2. **Dry run / document validate** — [plan.go:455](server/internal/handlers/checks/plan.go#L455)
   injects **unconditionally**, for any check whose `ConfigPrivateKeys` names a
   key the document omits. `ConfigPrivateKeys` is populated for every encrypted
   check, not just sealed-only ones ([service.go:4529](server/internal/handlers/checks/service.go#L4529)),
   and an export redacts secrets — so dry-running an org's **own export** of an
   ordinary AES-envelope HTTP check with `secretHeaders` fails, which is exactly
   the outcome spec 2026-09-11-04 added this injection to prevent.

The failure is silent to the operator in the only way that matters: the error
blames a field they did not send, and there is no way for them to make it pass.

## Proposal

Make the injected placeholder **shape-aware**, resolved from the checker's own
config rather than from a growing list of key names.

### Resolve the shape from the config struct

`registry.ParseConfig(checkerdef.CheckType(checkType))` already hands back the
typed config struct — it is what `credentials.SecretFieldsFor` is called on all
over this package. Reflect over that struct's fields, match the config key to a
field by its `json` tag, and pick the placeholder from the field's
`reflect.Kind`:

- `reflect.Map` → an **empty** `map[string]any{}`. Every map-shaped secret field
  is optional and none requires a non-empty map, so an empty map passes both
  decode and validation. It must be empty rather than populated: `checkgrpc`
  validates metadata key names (`grpc-` prefix reserved, `-bin` suffix rejected,
  RFC 7230 name pattern — [config.go:268](server/internal/checkers/checkgrpc/config.go#L268))
  and `checkhttp` rejects an empty header name ([checker.go:191](server/internal/checkers/checkhttp/checker.go#L191)),
  so any invented key is a new way to fail.
- `reflect.String` → `placeholderSecretValue`, unchanged — still the default, so
  no existing behaviour moves.
- anything else (or no matching field, or an unknown check type) → fall back to
  `placeholderSecretValue`, i.e. exactly today's behaviour. This must never be a
  hard error: the injector only widens or narrows what a throwaway `Validate`
  sees, it is not a source of truth.

Keep the `private_key` PEM special case as-is — it is a *value* rule, not a
shape rule, and reflection cannot derive it.

This means `injectSecretPlaceholders` needs the check type (or a resolved
shape lookup) as a parameter. Both call sites have it: `validatePatchedConfig`
takes `checkType`, and `plan.go` has `existing.Type`.

Weighed and rejected: a name→shape table (`secretHeaders`, `secretMetadata`,
`secrets` → empty map) — it works today but silently regresses the moment a
checker declares a fourth secret field, which is precisely how `private_key`
and these three got here. Also rejected: making each `FromMap` tolerate the
string placeholder — that weakens real input validation for everyone to serve
one internal caller.

### Tests that fail today

In `server/internal/handlers/checks/`:

1. **Sealed-only PATCH, per map-shaped field.** Create a check targeting a
   private region only, carrying `secretHeaders` (HTTP) — and a second one
   carrying `secrets` (JS) — so the row ends up sealed-only
   (`ConfigSealed != nil`, `ConfigPrivate == nil`) with the key named in
   `ConfigPrivateKeys`. PATCH something unrelated (the name, or the timeout) and
   assert the PATCH **succeeds**, that the sealed blob is preserved as-is, and
   that the stored public config still carries no secret. Add `secretMetadata`
   (gRPC) alongside them — it is the same defect and costs one more table row.
   Each of these fails today with the checker's own "must be a map…" message.
2. **Dry run / document validate.** Dry-run a document that omits
   `secretHeaders` against an existing *non-sealed* encrypted HTTP check whose
   `ConfigPrivateKeys` names it, and assert a clean dry run. This covers
   [plan.go:455](server/internal/handlers/checks/plan.go#L455), the call site the
   sealed-only tests do not reach.
3. **The string path is untouched.** A sealed-only check whose secret is a
   plain `password`, and one whose secret is `private_key`, still PATCH cleanly
   — a positive control proving the shape lookup did not simply stop injecting.
4. A unit test on the shape resolver itself: map field → empty map, string
   field → the string placeholder, unknown key and unknown check type → the
   string placeholder.

Run `make test` (backend) before calling it done; `make lint` must stay green.

## Implementation Plan

1. **Shape resolver + injector rewrite** (`server/internal/handlers/checks/service.go`):
   - Add `secretPlaceholderShapeFor(checkType, key string) any`: `private_key` keeps the PEM
     special case; otherwise `registry.ParseConfig(checkerdef.CheckType(checkType))`, reflect
     over the returned struct's fields (deref pointer), match the `json` tag (first
     comma-separated segment) against `key`; a `reflect.Map` field returns `map[string]any{}`,
     everything else (including unknown type / unknown key / no match) returns
     `placeholderSecretValue`.
   - Change `injectSecretPlaceholders(config map[string]any, keys []string)` to
     `injectSecretPlaceholders(checkType string, config map[string]any, keys []string)`,
     delegating the per-key value to `secretPlaceholderShapeFor`.
   - Update both call sites: `validatePatchedConfig` (service.go) and `planUpdateConfig`
     (plan.go) already have `checkType` in scope.
   - Add `"reflect"` to service.go's import block.
2. **Tests — map-shaped fields, sealed-only PATCH** (new file
   `server/internal/handlers/checks/patch_validate_sealed_map_secrets_test.go`): three cases
   mirroring `TestSealedOnlyPatchWithRequiredPasswordSecretUnrelatedFieldSucceeds` — HTTP with
   `secretHeaders`, gRPC with `secretMetadata`, JS with `secrets` — each sealed-only via a
   private region, PATCHed on an unrelated field, asserting success + blob preserved + public
   config still secret-free.
3. **Test — dry-run against non-sealed encrypted HTTP check omitting `secretHeaders`** (same
   new file or a plan_test.go addition): create a plaintext/AES-envelope HTTP check with
   `secretHeaders`, then dry-run (`PlanUpsert`/document-validate path hitting
   `planUpdateConfig`) a document that omits `secretHeaders`, asserting a clean pass — covers
   plan.go:455.
4. **Unit test on the shape resolver** (`server/internal/handlers/checks/service_test.go` or
   the new file): map field → empty map, string field → string placeholder, unknown key →
   string placeholder, unknown check type → string placeholder.
5. Positive controls for the string path (`password`, `private_key`) already exist in
   `patch_validate_sealed_test.go` — no new test needed, just confirm they still pass.
6. `make fmt`, then the scoped gate: `make build-backend lint-back test`.
