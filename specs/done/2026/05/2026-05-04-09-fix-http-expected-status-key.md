# Fix HTTP check `expectedStatus` silently ignored (camelCase / snake_case mismatch)

## Context

Concrete root cause for the user's report "When we set a 401 expected status and get a 401 we still return it as down":

1. The dash0 form writes `expectedStatus` (camelCase) into the check config:
   ```ts
   if (!isNaN(statusCode) && statusCode !== 200) cfg.expectedStatus = statusCode;
   ```
   `web/dash0/src/components/shared/check-form.tsx:396`

2. The backend's `FromMap` parser reads only `expected_status` (snake_case):
   ```go
   if expectedStatus, ok := configMap["expected_status"].(int); ok {
       c.ExpectedStatus = expectedStatus
   }
   ```
   `server/internal/checkers/checkhttp/config.go:90-97`

3. `cfg.ExpectedStatus` therefore stays `0`. The runner falls back to `200`:
   ```go
   expectedStatus := cfg.ExpectedStatus
   if expectedStatus == 0 { expectedStatus = 200 }
   ```
   `server/internal/checkers/checkhttp/checker.go:198-200`

4. A 401 response then fails `resp.StatusCode != expectedStatus` (`401 != 200`) and is reported `down`.

The repo convention (`CLAUDE.md`) states: **"Use camelCase consistently for both JSON properties and query parameters"** — so the bug is on the backend, not the frontend.

## Scope

**In scope:**
- Update `FromMap` in `server/internal/checkers/checkhttp/config.go` to read camelCase keys (`expectedStatus`, `expectedStatusCodes`, etc.). Keep snake_case as a deprecated read fallback for one release so already-stored configs still work.
- Audit *every* field parsed by `FromMap` (HTTP config) for the same camelCase/snake_case bug; fix all in this PR.
- Apply the same audit to **all** other check-type config parsers (`checkssl`, `checktcp`, `checkdns`, `checkmqtt`, `checksnmp`, `checkssh`, `checkemail`, …) — same class of bug, fix once.
- A one-shot data fixer that walks `checks.config` JSONB / JSON in the DB and renames any snake_case keys to camelCase. Idempotent.
- Tests: a config-parsing test asserting `FromMap({"expectedStatus": 401})` populates `cfg.ExpectedStatus = 401`. A checker test asserting a 401 response with `expectedStatus: 401` produces `up` (and same with `expectedStatusCodes: ["401"]`).

**Out of scope:**
- Removing the snake_case fallback entirely — defer that cleanup for one release after the data fixer has run on all environments.
- Frontend changes (the form is already correct per repo convention).

## Approach

### 1. Inventory

Grep for `configMap[".*_.*"]` and `configMap["[a-z]+"]` across `server/internal/checkers/*/config.go`. List every snake_case key currently read. For each, define the canonical camelCase equivalent (Go's `json:` tag is the source of truth where the struct already declares one).

### 2. Read both, prefer camelCase

In each `FromMap`, replace single-key reads with a small helper:

```go
func readKey[T any](m map[string]any, camel, snake string) (T, bool) {
    var zero T
    if v, ok := m[camel]; ok {
        if t, ok := v.(T); ok { return t, true }
    }
    if v, ok := m[snake]; ok {
        if t, ok := v.(T); ok { return t, true }
    }
    return zero, false
}
```

Apply uniformly. Keep the snake fallback only for keys that already exist in stored configs.

### 3. Data fixer

A new tool subcommand (or a standalone job): `solidping admin migrate-config-keys`. For each row in `checks`:
1. Read `config` as a generic map.
2. Walk known snake_case keys and rename them to camelCase.
3. Write back if anything changed; bump `updated_at` is fine.

Idempotent: running twice is a no-op. Log per-row diffs at debug level.

### 4. Tests

`server/internal/checkers/checkhttp/config_test.go`:
```go
func TestFromMap_ExpectedStatus_CamelCase(t *testing.T) {
    cfg, err := FromMap(map[string]any{"url": "http://x", "expectedStatus": 401})
    require.NoError(t, err)
    require.Equal(t, 401, cfg.ExpectedStatus)
}

func TestFromMap_ExpectedStatusCodes_CamelCase(t *testing.T) {
    cfg, err := FromMap(map[string]any{"url": "http://x", "expectedStatusCodes": []any{"401"}})
    require.NoError(t, err)
    require.Equal(t, []string{"401"}, cfg.ExpectedStatusCodes)
}
```

`server/internal/checkers/checkhttp/checker_test.go` — extend `TestHTTPChecker_Execute_ExpectedStatusCodes` (line ~1309) with:
- `{"expectedStatus": 401}` + 401 response → `up`.
- `{"expectedStatusCodes": ["401"]}` + 401 response → `up`.
- `{"expectedStatusCodes": ["4XX"]}` + 401 response → `up`.

Tests for the snake_case fallback so we don't regress legacy configs:
- `{"expected_status": 401}` + 401 response → `up`.

### 5. Manual sanity

After the fix, point a check at `https://httpbin.org/status/401` with `expectedStatus: 401`. It should run `up`.

## Verification

1. `make test` passes the new tests.
2. Run the data fixer against a dev DB; verify configs that had `expected_status: 401` now have `expectedStatus: 401`.
3. Create a new check via dash0 with `expectedStatus: 401` against an endpoint returning 401 → `up`.
4. Existing checks with stored `expected_status: …` (legacy) still run correctly until the data fixer runs.
