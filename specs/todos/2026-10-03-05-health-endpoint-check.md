---
model: sonnet
effort: medium
---

# `health` check type: read an application's health endpoint and report per component

## Problem

Most application frameworks already expose a health endpoint that lists the
state of each dependency (database, cache, queue, disk). Today the only way to
watch one is an `http` check with JSONPath assertions
(`checkers/checkhttp/config/config.go:121`, `json_path_assertions`). That gives
one pass/fail for the whole check: the user has to know the format, write the
assertions by hand, and the alert says "assertion failed" instead of "Redis is
down".

Oh Dear sells this as "application health monitoring" on every plan
(`wiki/competitors/ohdear.md`), with its own JSON format produced by
`spatie/laravel-health` and `ohdearapp/health-check-results`. Supporting that
format lets those users switch by changing a URL.

## Proposal

A new check type `health`. It sends the same request an `http` check would,
parses the body with one of several **existing** health formats, and turns the
result into a list of components. One check, one incident; components live in
the result output. No migration, no new endpoint.

### 1. Supported formats

New package `checkers/checkhealth/formats/`, one file per format, each with a
`Detect(contentType string, body map[string]any) bool` and a
`Parse(body) (Report, error)`:

| `format` | Produced by | Detection | Component statuses → ours |
|---|---|---|---|
| `spatie` | `spatie/laravel-health`, `ohdearapp/health-check-results` (Oh Dear format) | `checkResults` array present | `ok`→ok, `warning`→warning, `failed`/`crashed`→failed, `skipped`→skipped |
| `spring` | Spring Boot Actuator `/actuator/health` | top-level `status` + `components` (or legacy `details`) object | `UP`→ok, `DOWN`/`OUT_OF_SERVICE`→failed, `UNKNOWN`→unknown. Nested `components` are flattened as `parent.child`. |
| `ietf` | IETF `draft-inadarei-api-health-check` | `Content-Type: application/health+json`, or `checks` object whose values are arrays | `pass`→ok, `warn`→warning, `fail`→failed. One component per `checks` key (`component:measurement`); multiple entries under a key are suffixed with their `componentId`. |
| `aspnet` | ASP.NET Core HealthChecks (UI response writer) | `entries` object | `Healthy`→ok, `Degraded`→warning, `Unhealthy`→failed |
| `microprofile` | MicroProfile Health (Quarkus, Open Liberty) | `checks` array of `{name,status}` | `UP`→ok, `DOWN`→failed |
| `simple` | anything else | top-level `status` string only | `ok`/`up`/`pass`/`healthy`→ok, `warn`/`degraded`→warning, everything else→failed. No components. |

`format: auto` (default) tries them in the order above and uses the first
match. Detection is on JSON shape, never on the URL.

Common model:

```go
type Report struct {
    Format     string
    Overall    ComponentStatus   // as reported by the app, if any
    FinishedAt *time.Time        // spatie finishedAt, ietf time, when present
    Components []Component
}

type Component struct {
    Name, Label, Message, Summary string
    Status ComponentStatus            // ok | warning | failed | skipped | unknown
    Meta   map[string]any
}
```

Each component's `Message` is the format's human text: spatie
`notificationMessage`, ietf `output`, aspnet `description`, spring `details.error`
when present. `Summary` is spatie `shortSummary`, or `observedValue` +
`observedUnit` for ietf.

### 2. Config

New package `checkers/checkhealth/` (`checker.go`, `config/`, `samples.go`,
`alias.go`, same layout as `checkhttp/`). `HealthConfig` **embeds**
`checkhttp/config.HTTPConfig` so every request option comes for free (URL,
method, headers, `secretHeaders` encrypted at rest, basic auth, TLS options,
redirects, IP version, tunnels), and adds:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `format` | string | `auto` | One of the formats above. |
| `max_age` | duration | 10m | Results whose `FinishedAt` is older than this are stale. Ignored when the format has no timestamp. 0 disables. |
| `ignore` | []string | none | Component names to ignore (exact match, ≤ 50). |
| `components` | map name → `{on_failed: "down"\|"warning"}` | none | Per-component downgrade. |

The body assertions of `HTTPConfig` (`body_expect`, `body_pattern`,
`json_path_assertions`, …) are refused by validation on a `health` check, so
there is one way to judge the body. `expected_status` / `expected_status_codes`
are refused too (see 3).

Register in `checkers/checkerdef/types.go`: a `CheckTypeHealth CheckType =
"health"` constant next to `CheckTypeHTTP` (`:149`) and a table entry modelled
on the `http` one (`:389`), with `DefaultPeriod: time.Minute` and the same
`SupportsTunnel` / `SupportsIPVersion` flags.

### 3. Reusing the HTTP request

Do not duplicate the HTTP stack. `HTTPChecker.executeRequest`
(`checkers/checkhttp/checker.go:213`) already reads the body (capped at
`maxBodySize`, 10 MB, `:31`, `:457-461`) but does not return it. Add an opt-in
body capture to `responseInfo` (e.g. `info.captureBody bool`, `info.body []byte`,
`info.statusCode int`, `info.contentType string`), set by the health checker
only, and export a small entry point the health checker calls with its embedded
`HTTPConfig`. Network failures, timeouts and their diagnostics
(`refineHTTPNetworkFailure`, `locateHTTPNetworkFailure`) then behave exactly as
for `http`.

Health endpoints answer **503 when unhealthy, with the JSON body** (Spring,
ASP.NET). So the health checker accepts any status code that comes with a
parseable body, and judges by the body. A 503 with a non-JSON body is down.

### 4. Status rules

In order, first match wins:

1. Network error / timeout → as `http` today.
2. Body not JSON, or no format matches (`auto`), or the forced `format` fails to
   parse → **down**, `error: "health response not recognised"` (include the
   HTTP status code).
3. `max_age > 0` and `FinishedAt` older than `max_age` → **down**,
   `error: "health results are stale (last run 23 min ago)"`. This catches the
   scheduled job that computes the results having stopped.
4. Any non-ignored component `failed` whose `on_failed` is `down` (default) →
   **down**.
5. Any non-ignored component `warning`, or `failed` with `on_failed: warning`
   → `StatusWarning` (counts as up, opens no incident,
   `checkerdef/types.go:62-66`).
6. No components (`simple` format): use `Overall`, mapped as above.
7. Otherwise **up**.

`skipped` and `unknown` components are shown and never change the status.

### 5. Output, metrics, notifications

Output (in addition to what the HTTP part stamps):

```json
{
  "format": "spatie",
  "finished_at": "2026-10-03T09:12:00Z",
  "components": [
    {"name": "Database", "status": "ok", "summary": "12 ms"},
    {"name": "UsedDiskSpace", "status": "failed", "summary": "91%",
     "message": "The disk is almost full (91% used)"}
  ],
  "failed": ["UsedDiskSpace"],
  "warning": [],
  "error": "UsedDiskSpace: The disk is almost full (91% used)"
}
```

- At most 100 components kept in the output; `components_truncated: true` past
  that.
- `error` lists failed components as `Name: message`, joined with `; `, so the
  existing notification templates show it with no template change.
- **Metrics:** `components_total`, `components_failed`,
  `components_warning`, plus numeric `Meta` values as
  `meta.<component>.<key>`, at most 20 per check (same cap as Oh Dear), first
  20 in component order. Non-numeric meta stays in the output only.
- **Incident updates:** when the set of failed components changes during an
  open incident (Database then also Cache), the change must show up as an
  incident update rather than being silent. Check whether the incident engine
  already reacts to an `error` change on a still-down check (To verify); if not,
  add that for `health` only.

### 6. Dashboard (`web/dash0`)

- Check form for `health`: the HTTP request section (shared with `http`), then
  format (auto + list), `max_age`, ignored components, per-component downgrade.
  After a test run (the existing "validate" / sample flow), offer the
  component names found as suggestions for `ignore` and the overrides.
- Check detail: one row per component with status dot, label, summary and
  message; a small per-component timeline built from the last results' outputs
  (no new table).
- EN and FR strings.

### 7. Samples

`checkers/checkhealth/samples.go`: one sample per format (Laravel
`/health` with the Oh Dear secret header in `secretHeaders`, Spring
`/actuator/health`, ASP.NET `/healthz`, IETF `/health`).

## Tests

- `checkers/checkhealth/formats/*_test.go`, from fixture files in
  `testdata/` (real payloads from each framework's docs):
  - each format: detection true on its fixture, false on every other fixture;
  - status mapping of every value listed in the table, including `crashed`,
    `OUT_OF_SERVICE`, `Degraded`, `skipped`;
  - spring nested components flattened as `parent.child`;
  - ietf multiple entries under one key get distinct names;
  - `simple` with an unknown status word → failed.
- `checkers/checkhealth/checker_test.go` (httptest server):
  - 200 + all ok → up; 503 + spring `DOWN` body → down with the failing
    component in `error`; 503 + HTML body → down "not recognised";
  - forced `format: spring` on a spatie body → down;
  - spatie `finishedAt` 11 min old with `max_age: 10m` → down stale; `max_age: 0`
    → not stale;
  - a failed component in `ignore` → up; with `on_failed: warning` →
    `StatusWarning`;
  - a warning component → `StatusWarning`;
  - more than 100 components → truncated flag; more than 20 numeric meta
    values → only 20 metrics;
  - `secretHeaders` value is sent and never appears in the output.
- `checkers/checkhealth/config` tests: round-trip; validation refuses body
  assertions, `expected_status`, an unknown `format`, an invalid `on_failed`.
- `checkers/checkhttp` tests: body capture is off by default (no behaviour
  change for `http`, no body kept in memory).
- Registry test: `health` is registered and listed by the check-types API.

## To verify

- The cleanest seam in `executeRequest` for the body capture: where the body is
  read (`checker.go:457-461`) and how the status-code check exits early, so a
  503 still reaches the body read for `health`.
- Whether embedding `HTTPConfig` keeps `FromMap` / `GetConfig` / validation and
  the secret-header redaction (`checkhttp/config/redact.go`) working, or whether
  the health config must delegate to them explicitly.
- Whether the incident engine already posts an update when a down check's
  `error` text changes.
- The `web/dash0` components for the http check form, to share the request
  section.

## Open questions

1. **Default `max_age`.** Recommended: 10 minutes, Oh Dear's rule, so spatie
   users get the same behaviour. Formats without a timestamp skip the rule.
2. **Should `warning` components open an incident?** Recommended: no, they map
   to `StatusWarning` like a certificate close to expiry. A user who wants an
   alert can't currently promote a warning to down; leave that for later unless
   asked.

## Resolved open questions

Answered unattended on 2026-10-03 with the spec's recommended answers; to be reviewed by the owner.

1. Default `max_age` is 10 minutes. Formats without a timestamp skip the rule.
2. `warning` components do not open an incident: they map to `StatusWarning`, like a certificate close to expiry. No warning-to-down promotion in this spec.

## Out of scope

- One check (and incident, status-page resource, maintenance window) per
  component. Later, the discovery feature could suggest passive child checks
  fed by the parent's result; only if users ask.
- Push mode: letting a heartbeat push (`heartbeatpush/`) carry a health body
  for apps we can't reach. Natural follow-up once the parsers exist.
- Non-JSON formats (Kubernetes `/readyz?verbose` text, Prometheus metrics).
- A `/migrate/oh-dear` page on the website. That is marketing work in
  solidping-business once this ships.
