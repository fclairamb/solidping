---
model: opus     # haiku | sonnet | opus
effort: high    # low | medium | high
---

# Checks cannot send a fresh value per run, so a CDN cache can answer instead of the origin

## Problem
A check behind a CDN or a caching proxy often gets a cached 200 while the origin is down. The usual fix is a value that changes on every request (`?cb=<timestamp>`, an `X-Request-Id: <uuid>` header). SolidPing has no way to generate one. The only templating is `${env:NAME}` and `${param:KEY}`, both static values (`server/internal/secretref/secretref.go:20-36`).

How references flow today:
- `Pattern` is `\$\{(env|param):([^}]+)\}` (`secretref.go:36`). `ResolveConfig` walks every string, nested maps and slices included (`:148-200`).
- `${param:}` is resolved on the API at claim time into an overlay (`server/internal/checkworker/checkjobsvc/paramrefs.go:33-61`, called from `checkworker/backend/direct.go:189` and `handlers/agentws/handler.go:1179,1235`). `APIResolver` skips every other scheme (`secretref/resolvers.go:111-119`).
- `${env:}` is resolved by the executing process in `materializeConfig` (`server/internal/checkworker/worker.go:908-932`, called at `:1028`), with `ExecutionResolver`, which fails on any other scheme (`resolvers.go:126-134`). The same resolver is used for tunnels (`checkworker/backend/ws_tunnel.go:50`, `integrations/sshtunnel/sshtunnel.go:249`).
- Manifest apply proves every reference resolves with `DocumentResolver` (`handlers/checks/apply.go:320-350`, `resolvers.go:93-104`).
- Other `Pattern` users treat any match as a secret: plan masking (`handlers/checks/diff.go:534-543`) and the inlined-credential exemption (`handlers/checks/validate_document.go:509`).

What is stored in result output today: the HTTP checker writes `cfg.URL` into `Output["url"]` (`server/internal/checkers/checkhttp/checker.go:422,439,451,468,530,574,586,599,614`). `cfg` is the fully materialized config, and the worker submits `result.Output` untouched (`worker.go:1581`). So a `${param:token}` in a URL is already stored resolved in `results.output.url`, and from there reaches incident failure details (`handlers/incidents/failure_details.go:123-128`). Adding generated values to URLs makes that path more used, so this spec fixes it for `url`.

## Proposal
1. Grammar (`server/internal/secretref/secretref.go`):
   - Add `SchemeVar = "var"` and extend `Pattern` to `(env|param|var)`. One grammar, one traversal.
   - Add `IsSecretScheme(scheme) bool` (env, param) and `ContainsSecretRef(value) bool`.
   - Known names: `timestamp`, `iso8601`, `uuid`, `random`. Lowercase, case-sensitive. Export `KnownVars` for validators.
2. Generator (`server/internal/secretref/vars.go`, new): `NewVarResolver(now time.Time, rnd io.Reader)` returns a resolver that memoizes per name, so every occurrence in one walk gets the same value.
   - `timestamp`: UTC, 17 digits `YYYYMMDDhhmmssSSS`.
   - `iso8601`: UTC to the second, `2026-10-12T14:30:05Z`.
   - `uuid`: v4 from `github.com/google/uuid` (already in `server/go.mod`).
   - `random`: 8 chars from `[a-z0-9]`, `crypto/rand`. Lowercase so it is safe in hostnames and case-insensitive paths.
   - Unknown name: `Unresolvedf(SchemeVar, name)`.
3. Resolvers (`server/internal/secretref/resolvers.go`):
   - `ExecutionResolver()` creates one var resolver per call and handles `var` with it, then `env` as today. `materializeConfig` calls it once per job (`worker.go:924-925`), so one run shares one set of values. Tunnels get their own set, which is fine.
   - `APIResolver`: no change, it already skips `var`. The overlay keeps `${var:…}` literal and the worker expands it.
   - `DocumentResolver`: accept a known `var` name (return a generated sample), reject an unknown one.
4. Callers that mean "secret" switch to `ContainsSecretRef`:
   - `diff.go:543` masks only env/param references. `${var:uuid}` shows in clear in a plan.
   - `validate_document.go:509` exempts a credential-looking key only for an env/param reference, not for `${var:…}`.
   - `apply.go:332-334` only looks for `env`, no change.
5. Validation of unknown names:
   - `configValidationErrors` (`server/internal/handlers/checks/validate.go:381`) gets `validateVarRefs(effective)`: walk with `VisitStrings`, error `config.url: unknown variable ${var:foo} (known: timestamp, iso8601, uuid, random)`. That function feeds both the write paths and `validate_check`, so they agree.
   - `ValidateDocument` (`validate_document.go:163`, used offline by `sp checks validate`) gets the same pure check per check.
6. Output without secrets (`server/internal/checkworker/worker.go`):
   - In `materializeConfig`, before merging the param overlay, render `config["url"]` with the same var resolver and leave env/param references literal. Keep it on the job (not persisted).
   - Before building the submit request (`:1560-1581`), if `Output["url"]` equals the fully resolved URL and the configured URL held an env/param reference, replace it with that rendering. The stored URL shows the generated values and `${param:…}` in place of the secret.
7. Docs:
   - `web/docs/docs/features/config-as-code.md:117-123`: a "Generated values" subsection with the four names, formats, "same value within one run, new value each run, each region gets its own", an example `url: https://acme.com/health?cb=${var:timestamp}`, and that a deported agent older than this release reports `unresolved secret reference: var:…` as an error result.
   - `:49`: masking applies to `${param:…}` / `${env:…}` only.
   - `web/docs/docs/features/check-types.md` HTTP table (`:18-26`): one line pointing to the subsection.

## Tests
- `server/internal/secretref/vars_test.go`:
  - each name matches its regex (`^\d{17}$`, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, UUID v4, `^[a-z0-9]{8}$`), with an injected clock for the two time formats;
  - two `${var:uuid}` in one config (URL and a header) resolve to the same value;
  - two `ExecutionResolver()` instances give different `uuid` and `random`;
  - negative: `${var:nope}` and `${var:UUID}` fail with `ErrUnresolved`.
- `server/internal/secretref/secretref_test.go`: `Pattern` recognizes `${var:uuid}`. `APIResolver` skips it (next to `TestAPIResolverSkipsEnv`). `ContainsSecretRef` is false for a var-only string, true when a param ref is also present.
- `server/internal/handlers/checks/validate_findings_test.go`: `validate_check` with `${var:nope}` returns an error finding on `config`; with `${var:uuid}` returns none. Same pair through create.
- `server/internal/handlers/checks/validate_document_endpoint_test.go`: unknown var is reported for its check, known var passes (next to `:269`).
- `server/internal/handlers/checks/plan_semantics_test.go`: a plan shows `${var:timestamp}` in clear and still masks `${param:x}`.
- `server/internal/checkworker/secret_refs_e2e_test.go` (next to `TestRealHTTPCheckSendsTheResolvedBody`, `:203`): an httptest server records the query and an `X-Request-Id` header for `?cb=${var:uuid}` + `X-Request-Id: ${var:uuid}`. Assert both carry the same UUID, and a second run sends a different one.
- Same file: URL `https://…/?token=${param:tok}&cb=${var:random}`. Assert the stored `Output["url"]` contains `${param:tok}` and the generated random, and not the param value. Negative control: the server did receive the real param value.

## To verify
- Other checkers echo resolved config strings (a host, a DSN) into `Output`. This spec only fixes HTTP `url`. List the others in a follow-up if any echo a field that can hold a `${param:}`.
- Multi-step checks run one slice per claim (`worker.go:1034`). Each slice gets its own values. Confirm no step checker relies on a value staying the same across slices.

## Open questions
- Namespace `var` or `gen`? Recommended: `var`. It reads naturally in a URL and matches how operators describe it.
- `${var:timestamp}` as compact UTC digits or Unix epoch milliseconds? Recommended: compact UTC digits as specified. It is readable in a stored URL. Epoch can be added later under its own name if asked.
