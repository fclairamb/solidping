---
model: sonnet
effort: medium
---

# HTTP checks can only regex-match a response header, so "header must be absent" or "must not contain" cannot be expressed

## Problem
The only response-header assertion is `headers_pattern`, a map of header name to RE2 regex (`server/internal/checkers/checkhttp/config/config.go:100`). It fails when the header is missing (`server/internal/checkers/checkhttp/checker.go:669-693`). So a user cannot say:
- `X-Powered-By` must not be sent (not exists),
- `Cache-Control` must not contain `no-store` (not contains, and fine if absent),
- `Content-Type` equals `application/json`, ignoring case, without writing an anchored regex.

The dashboard cannot edit headers at all: the HTTP form leaves `headers_pattern` unowned so it survives a save untouched (`web/dash0/src/components/checks/form/types/http.tsx:853-854`), and only offers JSONPath and body assertions (`http.tsx:790-819`).

The body side already solved this. `bodyAssertions` is an `AssertionNode` tree (`config/jsonpath.go:62-75`) with `and`/`or` groups, `ignoreCase`, and the operators `eq`, `neq`, `contains`, `not_contains`, `regex` (`config/bodyassertions.go:28-35`). The tree also knows `exists` / `not_exists` (`jsonpath.go:40-41`). The dashboard has an editor for it (`web/dash0/src/components/checks/json-assertion-editor.tsx:130-145`) and a result card (`web/dash0/src/components/checks/json-assertion-result-card.tsx:12-16`).

## Proposal
1. Config. Add `HeaderAssertions *AssertionNode` with JSON key `headerAssertions` to `HTTPConfig` (next to `BodyAssertions`, `config/config.go:131`). Parse it in `FromMap` with `resolveKey(configMap, "headerAssertions", "header_assertions")`, like `bodyAssertions` (`config.go:365-375`). Emit it in `GetConfig` (`config.go:581-585`). A leaf's `path` holds the header name. Same tree as body and JSONPath assertions, so groups, `ignoreCase`, the editor and the result card are reused.
2. Validation, new `config/headerassertions.go` with `ValidateHeaders()` mirroring `ValidateBody` (`bodyassertions.go:133-180`), called from `ValidateSpec` next to the body block (`config/validate.go:123`), error field `headerAssertions`:
   - `path` required and a valid header field name (RFC 9110 token).
   - Operators: `eq`, `neq`, `contains`, `not_contains`, `regex`, `exists`, `not_exists`. Numeric operators rejected.
   - `value` required, except for `exists` / `not_exists` where it must be empty (same rule as `jsonpath.go:315-318`).
   - `regex` must compile through `compileAssertionRegex`. Groups must not be empty.
3. Evaluation, `EvaluateHeaders(h http.Header)` in the same file:
   - Lookup with `h.Values(path)`, so header names match case-insensitively. Each header line is one value. No splitting on commas: `Set-Cookie` and date values contain commas.
   - `exists`: at least one line. `not_exists`: none.
   - `eq`, `contains`, `regex`: pass when any value matches. Absent header: fail with error `header not found`.
   - `neq`, `not_contains`: pass when no value matches. Absent header: pass.
   - `ignoreCase` and the comparison go through the existing `compareValues`.
   - `actual` is the values joined with `, `, cut by `truncateActual` (`bodyassertions.go:59-66`). For a header that `shouldRedactHeader` flags (`checkhttp/capture.go:41-70`: `Set-Cookie`, `Authorization`, names with token/key/secret), report `[redacted]`. The comparison still runs on the real value.
4. Checker. Evaluate right after the `headers_pattern` block (`checker.go:669-693`), before body assertions. On failure return `failed(...)` with error `Header assertion failed` and the result tree under the output key `header_assertions`, same pattern as `errBodyAssertionFailed` / `outputKeyBodyAssertions` (`checker.go:40-46`, `:697-712`). Do NOT add it to `bodyDrivesAssertions` (`checker.go:498-500`): headers need no body read. It runs before `info.verdictReached` (`checker.go:738`), so health checks get it exactly as they get `headers_pattern` (which `checkhealth/config/validate.go:13-24` does not refuse).
5. `headers_pattern` keeps working, unchanged, evaluated first. Both can coexist on one check.
6. Schema and API. Regenerate `server/internal/checkers/schemas/http.json` (`go generate ./internal/checkers/schemas/...`, `server/internal/checkers/AGENTS.md` step 6). Add `headerAssertions` to the HTTP config description in `server/internal/app/openapi/openapi.yaml:10941-10960`.
7. Dashboard:
   - `json-assertion-editor.tsx`: add `HEADER_ASSERTION_OPERATORS` and a `HeaderAssertionEditor` (like `BodyAssertionEditor`) with `showPath`, `testIdPrefix="header-assertion"` and a new `pathPlaceholder` prop (`Header-Name` here, `$.path` stays the default at `:183`). Every element already gets a `data-testid` from the prefix (`:107-337`); keep it that way for anything new.
   - `http.tsx`: `headerAssertions` in `HttpState`, a `seedHeaderAssertions` reading both spellings (mirror `:156-165`), `toConfig` omit-to-clear (mirror `:298-303`), both spellings in `ownedKeys` (`:850-878`). `headers_pattern` stays unowned. New "Header assertions" section just before body assertions (`:808`). Add "header assertions" to `httpOptionsSummary` (`:839`).
   - `json-assertion-result-card.tsx`: export `HEADER_ASSERTION_RESULT_OUTPUT_KEY = "header_assertions"`; render it on the check detail page and keep it out of the generic output list, like the body key (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1857`, `:1960`).
   - Add `HeaderAssertionEditor` to `design-reference.tsx:88`. Strings via `t()` in en/fr/de/es.
8. Docs, `web/docs/docs/features/check-types.md`: a "Header assertions (`headerAssertions`)" subsection under Response assertions (before `##### The flat matchers`, `:204`), with the operator table, the absent-header rule per operator, any-match on repeated headers, case-insensitive names, redacted actual values, and a YAML example. Point the `headers_pattern` row (`:215`) at the new key.

## Tests
- `server/internal/checkers/checkhttp/config/headerassertions_test.go`: validation rejects an empty or invalid header name, `gt`, `eq` without value, `exists` with a value, a bad regex, an empty group; accepts each of the seven operators.
- Same file, `EvaluateHeaders` table, each op with a pass and a fail case: `eq` (and `eq` + `ignoreCase`), `neq`, `contains`, `not_contains`, `regex`, `exists`, `not_exists`. Absent header: `eq` / `contains` / `regex` / `exists` fail, `neq` / `not_contains` / `not_exists` pass. Lowercase `cache-control` finds `Cache-Control`. Two `X-Acme` lines: `contains` matching only the second passes, `not_contains` matching only the second fails. `Set-Cookie` actual is `[redacted]`.
- `server/internal/checkers/checkhttp/checker_test.go` with `httptest`: a failing header assertion gives down, error `Header assertion failed` and a `header_assertions` output; a passing one gives up. `headers_pattern` and `headerAssertions` on one check are both enforced.
- `config/config_test.go`: `FromMap` / `GetConfig` round trip keeps `headerAssertions`; the `header_assertions` spelling is read.
- `server/internal/checkers/checkhealth`: a health check with a failing `headerAssertions` is down (parity with `headers_pattern`).
- `web/dash0/src/components/checks/form/types/http.test.ts`: seeding from both spellings, omit-to-clear, an API-set `headers_pattern` survives `toConfig`.
- `web/dash0/e2e/check-http-header-assertions.spec.ts` (mirror `check-http-body-assertions.spec.ts`): add a `not_exists` leaf on `X-Powered-By`, save, reload, the editor shows it and the saved config has `headerAssertions`.
- `make test`, `make lint`, `bun run lint:e2e && bun run typecheck:e2e`.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**
- Shape: reuse the assertion tree (leaf `path` = header name), or a flat list `[{name, operator, value, ignoreCase}]`? Recommended: the tree. It is what `bodyAssertions` and `json_path_assertions` use, so the editor, validation helpers and result card come for free, and `or` groups work. A flat list reads a bit better in YAML but needs its own editor and result rendering.
- Repeated headers: any-match for positive operators, all-must-pass for `neq` / `not_contains`? Recommended: yes. "Some `Set-Cookie` contains `Secure`" and "no `Cache-Control` line contains `no-store`" are the questions users ask.
