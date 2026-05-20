# HTTP check: secret request headers

## Context

`HTTPConfig` currently treats only the basic-auth `Password` field as a secret (declared in
`SecretFields()` in `server/internal/checkers/checkhttp/config.go:350`). Any header value passed
via the existing `Headers map[string]string` lands in the plain JSONB `checks.config` column,
visible to anyone with DB read access. The comment in `SecretFields()` even names this explicitly:
"Authorization headers and bearer tokens passed inside `headers` are a known V2 follow-up."

This spec is that V2. It unblocks:

- A future `http-claude-api-messages` sample using the free `POST /v1/messages/count_tokens`
  endpoint with an `x-api-key` header (follow-up to
  [`2026-05-18-06-claude-api-status-sample.md`](2026-05-18-06-claude-api-status-sample.md)).
- Any user-configured check against a Bearer-token-protected API.

## Honest opinion

The cleanest design is two separate fields (`Headers` + `SecretHeaders`) rather than a parallel
`SecretHeaderNames []string`. Keeping secret values co-located with their field (mirroring how
`Password` works) avoids the foot-gun of declaring a header secret without a value, or having a
value without the secret flag. The encryption infrastructure (`SplitConfig`, `MergeConfig`,
`credmigrate`) already handles whole map-valued keys transparently — no structural change needed.

The only new surface is the frontend masked input, which should mirror the password pattern.

## Goal

- New `SecretHeaders map[string]string` field in `HTTPConfig`.
- Values encrypted at rest via the existing `*_private` envelope (identical to `password`).
- GET response masks values; PATCH-without-key preserves, explicit empty/nil clears.
- Dash0 form adds a "Secret headers" section with password-type inputs.
- `credmigrate` picks up the new field automatically — no code change to that package.
- After this spec, a follow-up spec can add a disabled-by-default `http-claude-api-messages`
  sample (which also requires extending `CheckSpec` with an `Enabled bool` field so the sample
  starts disabled until the user adds their key).

## Non-goals

- Encrypting plain `Headers` values — users who want secrets should use `SecretHeaders`.
- Making all headers secret (per-key granularity is enough for API key use cases).
- `SecretHeaders` in test-mode samples.

## Design

### Backend: `HTTPConfig` changes

**`server/internal/checkers/checkhttp/config.go`**

Add the field:
```go
SecretHeaders map[string]string `json:"secretHeaders,omitempty"`
```

Update `SecretFields()`:
```go
func (c *HTTPConfig) SecretFields() []string {
    return []string{"password", "secretHeaders"}
}
```

Update `FromMap()` — add a block after the plain `Headers` extraction:
```go
if secretHeaders, ok := configMap["secretHeaders"].(map[string]string); ok {
    c.SecretHeaders = secretHeaders
} else if secretHeadersAny, ok := configMap["secretHeaders"].(map[string]any); ok {
    c.SecretHeaders = make(map[string]string, len(secretHeadersAny))
    for k, v := range secretHeadersAny {
        if strVal, ok := v.(string); ok {
            c.SecretHeaders[k] = strVal
        } else {
            return checkerdef.NewConfigErrorf("secretHeaders", "%s must be a string", k)
        }
    }
} else if configMap["secretHeaders"] != nil {
    return checkerdef.NewConfigError("secretHeaders", "must be a map[string]string")
}
```

Update `GetConfig()`:
```go
if len(c.SecretHeaders) > 0 {
    cfg["secretHeaders"] = c.SecretHeaders
}
```

Update `Validate()` — add a header name validity check for `SecretHeaders` (same rule as `Headers` if one exists, or a simple non-empty check):
```go
for k := range c.SecretHeaders {
    if k == "" {
        return errors.New("secret header name must not be empty")
    }
}
```

### Backend: execution merge

**`server/internal/checkers/checkhttp/checker.go`** — in `HTTPChecker.Execute`, after applying
plain `Headers`, apply `SecretHeaders` (secret wins on conflict):

```go
for k, v := range config.SecretHeaders {
    req.Header.Set(k, v)
}
```

The order (plain first, secret second) means a user cannot accidentally expose a secret by also
setting the same key in `Headers` — the secret value always wins.

### Encryption flow (no new code needed in credmigrate)

`credmigrate.migrateChecks` calls `credentials.SecretFieldsFor(cfg)` via the registry, which
calls `HTTPConfig.SecretFields()`. Adding `"secretHeaders"` to that list means `SplitConfig` will
automatically strip `secretHeaders` from the public map and put it in the private map on both
new writes (handler path) and the backfill migration.

The `MergeConfig` / `MergePatch` functions handle `map[string]any` values correctly — they treat
`secretHeaders` as an opaque value, which is what we want.

### API contract

- `GET /api/v1/orgs/:org/checks/:uid` — `config.secretHeaders` absent in response; key
  `"secretHeaders"` appears in `configPrivateKeys` list so the dashboard can render placeholders.
- `PATCH` without `secretHeaders` key → preserve existing encrypted values (handled by
  `MergePatch` secret-field semantics).
- `PATCH` with `"secretHeaders": {}` or `"secretHeaders": null` → clears all secret headers.
- `PATCH` with `"secretHeaders": {"x-api-key": "sk-xxx"}` → replaces the entire map.

### Frontend: dash0 form

**`web/dash0/src/components/shared/check-form.tsx`**

Add a "Secret headers" section in the HTTP form tab, below the plain Headers section. Render
each secret header as a row with a key input (type text) and a value input (`type="password"`
or equivalent masked primitive from the design reference). Allow add/remove rows. Map to the
`secretHeaders` key in the form payload.

Check `http://localhost:4000/dash0/orgs/default/design-reference` before creating any new
primitive — reuse whatever masked-input or key-value-pair pattern already exists.

The section should show placeholder indicators when `configPrivateKeys` contains
`"secretHeaders"` (i.e., "• • • •" or "set" badge), never the actual value.

**`web/dash0/src/locales/en/checks.json`** and **`fr/checks.json`** — add i18n keys:
- `"secretHeaders"` label
- `"secretHeadersDescription"` tooltip (e.g. "Header values stored encrypted — use for API keys and Bearer tokens.")
- `"addSecretHeader"` button label

## Files to change

### Modified files
- `server/internal/checkers/checkhttp/config.go` — new field, `SecretFields()`, `FromMap()`, `GetConfig()`, `Validate()`
- `server/internal/checkers/checkhttp/checker.go` — merge `SecretHeaders` in `Execute()`
- `web/dash0/src/components/shared/check-form.tsx` — secret-headers UI section
- `web/dash0/src/locales/en/checks.json` — i18n
- `web/dash0/src/locales/fr/checks.json` — i18n

### Files that need no change
- `server/internal/credmigrate/credmigrate.go` — already handles new secret keys automatically
- `server/internal/crypto/credentials/secret_fields.go` — generic; no change

## Tests

**Backend unit tests** (`server/internal/checkers/checkhttp/`):
- `FromMap` round-trips `SecretHeaders` correctly.
- `SplitConfig(GetConfig(), SecretFields())` moves `secretHeaders` to private and not to public.
- `MergePatch` with absent `secretHeaders` preserves existing; explicit empty clears.
- `Execute` applies secret headers after plain headers; secret wins on same key.

**Playwright** (`web/dash0/e2e/`):
- Create an HTTP check with a secret header via the form; confirm the API response for that
  check does not include the header value, only the key in `configPrivateKeys`.
- Edit the check; confirm the UI shows placeholder rather than the value.
- Update the header value; confirm it persists correctly.

## Verification

```bash
make lint && make test
```

API smoke test (encrypted storage):
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create check with secret header
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Claude count-tokens","slug":"claude-count-tokens","type":"http",
       "config":{"url":"https://api.anthropic.com/v1/messages/count_tokens",
                 "method":"POST",
                 "secretHeaders":{"x-api-key":"sk-ant-test"}}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '{config:.config, privateKeys:.configPrivateKeys}'
# Expect: config has no x-api-key, configPrivateKeys contains "secretHeaders"
```

## Risk log

| Risk | Mitigation |
|---|---|
| Existing checks with `headers` containing API keys are not auto-migrated | `credmigrate` only migrates declared secret fields; plain `headers` are intentionally not secret. Document that users should move keys to `secretHeaders` manually. |
| `SecretHeaders` map replace-on-PATCH semantics surprise users | Match the same UX as `Headers` — clearly document in API spec and tooltip that PATCH replaces the entire map. |
| Frontend shows stale placeholder after update | Follow the same pattern as the password field — invalidate the check query after a successful PATCH. |
| `CheckSpec` has no `Enabled bool` field, so a future count_tokens sample would start enabled and fail with 401 | The follow-up spec must also extend `CheckSpec` with `Enabled bool` and thread it through `createSampleCheck` in `job_startup.go`. Track as a prerequisite. |
