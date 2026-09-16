---
model: opus
effort: high
---

# HTTP body text assertions: exact match, case-insensitive, and a UI for the matchers that already exist

*Reported by **Jens**, an early user and one of the first people to run SolidPing
for real, in reply to a "what do you think?" email on 2026-09-16. This is the single feature he
asked for, and the only item in his feedback that is a missing capability rather than a
navigation problem.*

## What Jens asked for

> One thing I am missing is to be able to check the return value from an endpoint. For example I
> have health checks on some of my services, like `https://<his-host>/DictionaryApi/health`, this
> endpoint returns one of 3 values: "HEALTHY, DEGRADED, UNHEALTHY" and I would like to have a
> check if the return value exactly matches "HEALTHY" then all is good, otherwise there is an
> incident.
>
> You currently have "JSON Assertions", but I would like "Text Assertions" (with ignore casing).

He is modelling the ASP.NET Core health-check convention, where the endpoint returns a bare
`HealthStatus` word as `text/plain`:

- https://learn.microsoft.com/en-us/aspnet/core/host-and-deploy/health-checks
- https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.diagnostics.healthchecks.healthstatus

This is not a niche shape. `AddHealthChecks().MapHealthChecks("/health")` with the default
writer is what every ASP.NET Core service ships with, and it returns exactly `Healthy`,
`Degraded` or `Unhealthy` as plain text with a 200 for the first two. A monitoring tool that
can only assert on the status code reports a **degraded** service as up.

## Current state — the capability is half-built and completely invisible

### What exists in the backend

[`server/internal/checkers/checkhttp/config.go:60-127`](server/internal/checkers/checkhttp/config.go#L60-L127)
already carries plain-text body matchers:

| Field | JSON key | Semantics | Evaluated at |
|---|---|---|---|
| `BodyExpect` | `bodyExpect` / `body_expect` | `strings.Contains` — **substring**, case-sensitive | [checker.go:575-584](server/internal/checkers/checkhttp/checker.go#L575-L584) |
| `BodyReject` | `bodyReject` / `body_reject` | `strings.Contains`, negated | [checker.go:586-595](server/internal/checkers/checkhttp/checker.go#L586-L595) |
| `BodyPattern` | `bodyPattern` / `body_pattern` | RE2 `MatchString` | [checker.go:597-605](server/internal/checkers/checkhttp/checker.go#L597-L605) |
| `BodyPatternReject` | `bodyPatternReject` / `body_pattern_reject` | RE2, negated | [checker.go:607-617](server/internal/checkers/checkhttp/checker.go#L607-L617) |
| `HeadersPattern` | `headersPattern` / `headers_pattern` | RE2 per response header | [checker.go:619-644](server/internal/checkers/checkhttp/checker.go#L619-L644) |

### Why Jens could not find any of it

1. **There is no UI.** The HTTP check form models exactly one assertion editor — the JSONPath
   one ([`web/dash0/src/components/checks/json-assertion-editor.tsx`](web/dash0/src/components/checks/json-assertion-editor.tsx),
   mounted at [`form/types/http.tsx:595-612`](web/dash0/src/components/checks/form/types/http.tsx#L595-L612)).
   The five body/header matchers above are **deliberately unmodeled**: they survive edits only
   through the unmodeled-key passthrough (`httpModule.ownedKeys`,
   [http.tsx:641-663](web/dash0/src/components/checks/form/types/http.tsx#L641-L663), and
   [check-form.tsx:582-593](web/dash0/src/components/shared/check-form.tsx#L582-L593); see spec
   `specs/done/2026/09/2026-09-11-01-http-check-form-drops-unmodeled-config-keys.md`). A user
   who has never read the Go source cannot discover that `body_expect` exists.
2. **Nothing does exact equality.** `body_expect` is `strings.Contains`. For Jens's endpoint,
   `body_expect: HEALTHY` would match a body of `UNHEALTHY` — the exact inversion of what he
   wants, and a silent false-negative that reports a dead service as up. This is the sharpest
   part of the bug: the closest existing feature is actively wrong for his case.
3. **Nothing is case-insensitive.** Confirmed across the whole assertion path: `compareValues`
   ([jsonpath.go:157-176](server/internal/checkers/checkhttp/jsonpath.go#L157-L176)) uses `==`
   and `strings.Contains`; the body matchers use `strings.Contains` / `regexp.MatchString`.
   There is no `EqualFold`, no `ToLower`, no `ignoreCase` flag anywhere. The only escape hatch
   is hand-writing `(?i)` into a `body_pattern` regex — which, again, has no UI.
4. **The public docs advertise a key that does not exist.**
   [`web/docs/docs/features/check-types.md`](web/docs/docs/features/check-types.md) (~line 80)
   shows a YAML example using **`body_match:`**. There is no `body_match` in `HTTPConfig`, and
   `FromMap` silently ignores unknown keys — so anyone following that doc gets a check that
   asserts nothing and passes forever. This is its own fail-open bug and must be fixed as part
   of this spec.

### The trap that will silently break the implementation

[`checker.go:466-468`](server/internal/checkers/checkhttp/checker.go#L466-L468) gates whether the
response body is read at all:

```go
bodyDrivesAssertions := cfg.BodyExpect != "" || cfg.BodyReject != "" ||
    cfg.BodyPattern != "" || cfg.BodyPatternReject != "" || cfg.JSONPathAssertions != nil
```

**Any new body-reading assertion that is not added to this list fails open** — the assertion is
configured, the body is never read, and the check passes unconditionally. This exact bug already
shipped once for JSONPath assertions (`specs/done/2026/08/2026-08-20-04-http-jsonpath-assertions-silently-skipped.md`).
It must not ship twice.

## Proposal

### A. Backend — a body assertion with an operator, not another bespoke key

Resist adding `body_equals` + `body_equals_ignore_case` as two more flat keys. That is a third
parallel matcher vocabulary (flat substring keys, flat regex keys, JSONPath AST) for the same
job, and the next request ("not equals", "starts with") adds two more keys again.

Add a **`bodyAssertions`** node to `HTTPConfig` reusing the existing `AssertionNode` AST
([jsonpath.go:37-56](server/internal/checkers/checkhttp/jsonpath.go#L37-L56)), with `Path`
unused/ignored and the subject being the raw response body as a string:

```jsonc
{
  "bodyAssertions": {
    "type": "assertion",
    "operator": "eq",
    "value": "HEALTHY",
    "ignoreCase": true
  }
}
```

Requirements:

1. Add `IgnoreCase bool` (`ignoreCase` / `ignore_case`) to `AssertionNode`. Thread it through
   `compareValues` (`eq`, `neq`, `contains` → `strings.EqualFold` / a folded `Contains`) and
   through `regex` (prepend `(?i)` when set — do this by compiling with the flag, not by string
   concatenation, so a pattern with its own `(?i)` or an anchored group is not corrupted).
   This also earns case-insensitivity for **existing JSONPath assertions**, which is a real gap
   in its own right (`$.status eq "ok"` fails on `"OK"` today).
2. Support operators `eq`, `neq`, `contains`, `not_contains`, `regex` for body assertions, plus
   the `and`/`or` combinators the AST already has. `exists`/`not_exists` and the numeric
   operators are meaningless against a raw body — `validateLeaf` must **reject** them for this
   node with a clear `VALIDATION_ERROR`, not silently accept-and-always-pass.
3. Decide and document the whitespace rule, and make it explicit rather than implicit. A
   `text/plain` health endpoint very often emits a trailing newline; `eq "HEALTHY"` against
   `"HEALTHY\n"` failing is a support ticket waiting to happen. Recommendation: **trim leading
   and trailing whitespace from the body before `eq`/`neq` comparison**, leave `contains` and
   `regex` untrimmed, and say so in the field help text and the docs. If the implementer
   disagrees, the alternative (never trim, and document it) is acceptable — but the behavior
   must be tested and documented either way, not left to chance.
4. **Add `cfg.BodyAssertions != nil` to `bodyDrivesAssertions` at
   [checker.go:466](server/internal/checkers/checkhttp/checker.go#L466).** See the trap above.
5. Parse in `FromMap`, emit in `GetConfig` (camelCase only, matching the existing convention),
   validate in `Validate`.
6. **No migration.** Assertions live in the check's `config` JSONB/TEXT blob
   ([postgres 001_v0_1_0.up.sql:318](server/internal/db/postgres/migrations/001_v0_1_0.up.sql#L318),
   [sqlite 001_v0_1_0.up.sql:257](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql#L257)).
   Confirm the denormalized `check_jobs.config` copy picks it up without further work.
7. Keep `body_expect` / `body_reject` / `body_pattern` / `body_pattern_reject` working
   unchanged. They are load-bearing for the importers
   ([gatus.go:600-680](server/internal/handlers/checks/importers/gatus.go#L600-L680),
   `uptimekuma.go:351-360`, `uptimerobot.go:343`, `betterstack.go:518`) and for config-as-code
   files already in the wild. Do **not** migrate them.

### B. Frontend — a Body assertions section in the HTTP form

Follow the design reference first: `http://localhost:4000/d/orgs/default/design-reference`
(source [`design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx), the
existing assertion-editor entry is at lines ~5687-5726).

1. Generalize `json-assertion-editor.tsx` rather than forking it: it already renders a recursive
   `and`/`or` tree with an operator select
   ([lines 22-33, 89, 113-117](web/dash0/src/components/checks/json-assertion-editor.tsx#L22-L33)).
   Parameterize (a) whether the JSONPath `Path` input is shown, and (b) the allowed operator
   list. A body assertion row is then the same component with the path input hidden.
2. Add an **"Ignore case"** checkbox to each leaf row, wired to `ignoreCase`. It applies to the
   JSONPath editor too.
3. Mount a **Body assertions** section in the HTTP form's Advanced area next to JSON assertions
   ([http.tsx:595-612](web/dash0/src/components/checks/form/types/http.tsx#L595-L612)), with
   help text that states the whitespace rule and gives the ASP.NET example.
4. Add `bodyAssertions` to `httpModule.ownedKeys`
   ([http.tsx:641-663](web/dash0/src/components/checks/form/types/http.tsx#L641-L663)) so it
   round-trips as a modeled key rather than through the passthrough.
5. Render body-assertion outcomes on the check detail page alongside the JSONPath ones
   (`json-assertion-results.tsx`, `json-assertion-result-card.tsx`, mounted at
   [checks.$checkUid.index.tsx:1741](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L1741)).
   A failing assertion must say **what it expected and what it got** — a check that went down
   with "assertion failed" and no actual value is not debuggable.
6. Operator labels are currently rendered as raw untranslated strings
   ([json-assertion-editor.tsx:113-117](web/dash0/src/components/checks/json-assertion-editor.tsx#L113-L117)).
   Translate them as part of this work. **Every new key must be added to all four locales**
   (`en`, `fr`, `de`, `es`) or [`locale-parity.test.ts`](web/dash0/src/locales/locale-parity.test.ts)
   fails — and note that `bun run test:unit` is not part of the usual backend QA loop, so run it
   explicitly.
7. Add a sample config so the feature is discoverable: extend
   [`samples.go`](server/internal/checkers/checkhttp/samples.go) with an "ASP.NET Core health
   endpoint" HTTP sample using `bodyAssertions` + `ignoreCase`. This is also how the MCP server
   teaches the shape to an LLM — `get_check_type_samples` is the only place assertions are
   exposed to MCP at all ([`server/internal/mcp/tools_checktypes.go`](server/internal/mcp/tools_checktypes.go)).

### C. Docs — fix the lie, then document the feature

1. **Delete or correct `body_match:`** in
   [`web/docs/docs/features/check-types.md`](web/docs/docs/features/check-types.md). It does not
   exist and silently no-ops. Replace the example with a real one.
2. Document the full HTTP assertion surface in the public docs — today JSONPath assertions are
   not documented publicly at all, and the plain-text matchers only appear in the internal wiki
   ([`wiki/conventions/checker-config.md:49-54`](wiki/conventions/checker-config.md#L49-L54)).
   Add a "Response assertions" section covering status codes, body assertions (with the ASP.NET
   health-check example spelled out), JSONPath assertions, and the header matcher.
3. Update `wiki/conventions/checker-config.md` with `bodyAssertions` and `ignoreCase`.
4. The OpenAPI spec types `config` as a free-form object
   ([openapi.yaml:9748](server/internal/app/openapi/openapi.yaml#L9748)) and does not enumerate
   assertions. Leave that as-is — do not expand the spec's `config` description into a partial
   list that will rot; the sample endpoint is the contract.

### D. Tests

Backend:
- Table-driven `checkhttp` tests for every new operator × `ignoreCase` on/off, including the
  case that motivated this: body `"Healthy"` / `"HEALTHY\n"` / `"Unhealthy"` against
  `eq "HEALTHY"` with `ignoreCase: true` → up, up, **down**.
- **A positive control for the fail-open gate**: a check configured with only `bodyAssertions`
  (no other body key) against a body that must fail, asserting the result is `down`. If
  `bodyDrivesAssertions` is not updated this test fails — that is the whole point of it. A test
  that only asserts the passing case would go green with the bug present.
- Validation tests: numeric and `exists` operators on a body assertion are rejected with
  `VALIDATION_ERROR`.
- Round-trip test: `FromMap(GetConfig(cfg))` preserves `bodyAssertions` including `ignoreCase`.
- A regression test that `body_expect` and friends still work and still round-trip through the
  form's passthrough.

Frontend:
- Playwright e2e in `web/dash0/e2e/` (sibling of `check-http-json-assertions.spec.ts`): create an
  HTTP check with a body assertion via the form, save, reopen, confirm it round-trips, and
  confirm the ignore-case checkbox persists.
- `bun run test:unit` for locale parity.

While fixing this, also remove the dead `"body_contains"` key in
[`tunnel_test.go:55`](server/internal/checkers/checkhttp/tunnel_test.go#L55) — it is not a real
config key, `FromMap` drops it, and that test's body assertion has never run.

## Reply to Jens

He should be told three things: the exact-match + ignore-case assertion is being built and why
his case is a good one; that `body_expect`/`body_pattern` exist today as a workaround but are
substring/regex and unavailable in the UI, so he should wait rather than hand-edit YAML; and
that the docs page that mentions `body_match` was wrong, which is on us.
