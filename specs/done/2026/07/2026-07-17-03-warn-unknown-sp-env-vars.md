---
model: opus
effort: high
---

# Unknown `SP_*` env vars are silently ignored, so typo'd settings become no-ops in production

## Problem

Any `SP_`-prefixed environment variable that doesn't map to a recognized config
key is silently dropped. Nothing logs, nothing fails — the operator believes the
setting is applied, and it isn't.

This has already bitten us twice. The k8xp overlays set
`SP_RATE_LIMITING_TRUSTED_PROXIES`, missing the `SERVER_` segment:
`applyRateLimitingEnv` ([config.go:1004](server/internal/config/config.go:1004))
only reads `SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES`. The setting was a silent
no-op in production-like deployments.

The mechanism is the koanf env provider at
[config.go:869-876](server/internal/config/config.go:869):

```go
Prefix: "SP_",
TransformFunc: func(key, value string) (string, any) {
    return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(key, "SP_"), "_", ".")), value
},
```

Every `SP_*` var is accepted into the koanf map as a dotted path. Unknown names
aren't rejected at load — they're silently discarded later at `Unmarshal`
([config.go:879](server/internal/config/config.go:879)), which ignores keys with
no matching struct field. There is no error surface at all.

### The env surface is bigger than it looks

A recognized-name registry must union **four** independent sources. Missing any
one produces false-positive warnings, which would be worse than no check:

1. **koanf-reachable struct paths** — reflected from the `Config` struct
   ([config.go:241-274](server/internal/config/config.go:241)).
2. **Manual readers in `config.Load`** — `applyRateLimitingEnv`,
   `applyPasswordHashingEnv`, `applyServerEnv`, `applyAgentEnv`, `applyAuthEnv`,
   `applyJobsEnv`, `applyRealtimeEnv`, `applySchedulingEnv`, `applyProfilerEnv`,
   `applyDatabasePoolEnv`, `applyRuntimeEnv`, `applyFileStorageEnv`,
   `applyWebPushEnv`, plus bare reads inside `Load` itself (`SP_REDIRECTS`,
   `SP_RUN_MODE`, `SP_REGION`, `SP_SHUTDOWN_TIMEOUT`,
   `SP_SERVER_MAX_REQUEST_DURATION`, `SP_DB_RESET`, `SP_LOG_LEVEL`,
   `SP_APP_GITHUB_ISSUES_TOKEN`, `SP_APP_GITHUB_REPO`).
3. **The systemconfig parameter table** — `getKnownParameters()`
   ([systemconfig.go:173](server/internal/systemconfig/systemconfig.go:173)),
   **81 names** via its `EnvVar` field. This is where all of `SP_EMAIL_*`,
   `SP_LDAP_*`, `SP_OIDC_*`, `SP_SLACK_*`, `SP_DISCORD_*`, `SP_SAML_*`,
   `SP_BASE_URL`, `SP_AUTH_JWT_SECRET`, and `SP_AGGREGATION_RETENTION_*` are
   actually consumed. It is currently **unexported**.
4. **Ad-hoc `SP_PERFORMANCE_*` resolvers** — a second, parallel env→param chain
   in [job_aggregation.go:343-397](server/internal/jobs/jobtypes/job_aggregation.go:343)
   and [job_jobs_cleanup.go:86](server/internal/jobs/jobtypes/job_jobs_cleanup.go:86).
   Not in the systemconfig table.

There is **no choke point** to hook name registration into: `intEnv` / `strEnv` /
`durEnv` / `boolEnv` / `u32Env` / `u8Env` / `parseFloat` are re-declared as
function-local closures per reader (`intEnv` exists twice, at
[config.go:983](server/internal/config/config.go:983) and
[config.go:1058](server/internal/config/config.go:1058)), and everything else
calls `os.Getenv` inline. A declarative registry is the cheaper path than
hoisting the closures.

## Proposal

Add a startup check that scans `os.Environ()` for `SP_*` names, compares against
the recognized set, and logs one WARN per unrecognized variable with a
did-you-mean suggestion. **Never fail startup** — unknown vars are legitimate in
mixed fleets running newer/older binaries.

### Decision: "recognized" means *actually binds*, not *looks plausible*

The registry must contain names that have a real effect — not names that merely
resemble a config path. Given the `TransformFunc`, an env var is koanf-reachable
**iff every segment of its target path is a single word**: `SP_X_Y_Z` can only
ever produce `x.y.z`, so a snake_case koanf tag is unreachable via env unless a
manual reader or a systemconfig entry covers it. There are ~99 snake_case koanf
tags in `internal/config/*.go`.

So the reverse mapping is: for each koanf leaf path whose every segment is
underscore-free, the reachable name is `"SP_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))`.
Paths with a snake_case segment are **excluded** from the koanf-derived set and
must be earned via source 2 or 3.

Enumerating the paths is nearly free: [config.go:850](server/internal/config/config.go:850)
already loads the defaults through `structs.Provider(defaults, "koanf")`, and
koanf v2 exposes `Keys()`. Calling it immediately after the defaults load —
before any file or env load pollutes the map — yields every valid leaf path.
Reflection must special-case `koanf:"-"` fields (`Server.Redirects`,
`LogLevel`) so they don't become phantom recognized names.

This semantic is the whole point: a name that parses but never binds is exactly
the bug class we're chasing.

### Decision: plain edit-distance fails the required test case

A naive Levenshtein suggester with the usual threshold of ≤3 **will not** produce
the `SP_RATE_LIMITING_TRUSTED_PROXIES` → `SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES`
suggestion: inserting `SERVER_` is a distance of **7**. Loosening the threshold
to catch it would spray junk suggestions across every other name.

Use a two-rule matcher instead, ranked:

1. **Segment subsequence** — split both names on `_`. If the unknown's segments
   are a subsequence (or superset) of a known name's segments, suggest it. This
   catches the missing-segment and extra-segment cases precisely:
   `[RATE,LIMITING,TRUSTED,PROXIES]` ⊂ `[SERVER,RATE,LIMITING,TRUSTED,PROXIES]`
   yields exactly one candidate.
2. **Levenshtein ≤ 3** — for genuine typos (`SP_LOG_LEVL`).

Emit at most one suggestion (the best-ranked); when nothing matches, warn without
a suggestion. No new dependency — there is no fuzzy/edit-distance helper in the
repo and none in `server/go.mod`; hand-roll ~30 lines.

### Placement

`config.Load()` is `func Load() (*Config, error)`
([config.go:693](server/internal/config/config.go:693)) — no logger, no context,
and it runs before logging is configured. It also can't import `systemconfig`
(that package imports `config`; the dependency would cycle).

So put the check in a **new small package** that may import both `config` and
`systemconfig`, and call it from `serve` in `server/main.go`, between
`setupLogger(cfg.LogLevel)` ([main.go:100](server/main.go:100)) and
`cfg.Validate()` ([main.go:102](server/main.go:102)). Warn *before* `Validate()`:
a typo'd var is often the reason validation fails, so the hint must print before
a fatal exit. The codebase logs through the package-level default `slog`
(`setupLogger` calls `slog.SetDefault`), matching existing startup lines at
[main.go:118](server/main.go:118) and [main.go:131](server/main.go:131), so no
logger argument is needed.

Export a `systemconfig.KnownEnvVars() []string` accessor over
`getKnownParameters()`. It's a pure data function with no DB dependency, so it's
available at `main.go:100` even though `InitializeSystemConfig` only runs later
at [main.go:191](server/main.go:191).

### Must not flag (false-positive sources)

- **`${env:FOO}` references from check config-as-code** —
  [checks/apply.go:287](server/internal/handlers/checks/apply.go:287) resolves
  operator-supplied names via `os.LookupEnv`. An operator can legitimately export
  an arbitrary `SP_*` var and reference it from a check. Unknowable at startup;
  this is a core reason the check must be WARN-only.
- **CLI vars an operator may have exported in the same shell** — `SP_TOKEN`
  ([pkg/cli/commands.go:38](server/pkg/cli/commands.go:38)), `SP_SERVER`,
  `SP_ORG` ([scenariodriver/main.go:40](server/cmd/scenariodriver/main.go:40)).
  Allowlist by exact name.
- **Deliberate double-reads** — `SP_AUTH_PASSWORD_*` and
  `SP_SCHEDULING_FAST_LANE_RESERVED` are read both in `config.Load` and in the
  systemconfig overlay (documented at
  [config.go:1018-1033](server/internal/config/config.go:1018)). Dedup by name.

### Tests

Follow `server/CLAUDE.md` — `require.New(t)`, table-driven where it fits. Note
the existing config tests deliberately omit `t.Parallel()` because it is
incompatible with `t.Setenv`, carrying `//nolint:paralleltest` (e.g.
`TestRealtimeDefaults` at [config_test.go:405](server/internal/config/config_test.go:405)).
**Match that pattern for any test using `t.Setenv`**; use `t.Parallel()` on the
pure-function tests (the matcher, the reverse-mapper), which need no env.

Required cases:

- A **recognized** name produces no warning (`SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES`).
- An **unknown** name produces a warning (`SP_TOTALLY_MADE_UP`).
- The **near-miss** `SP_RATE_LIMITING_TRUSTED_PROXIES` warns *and* suggests
  `SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES`. This is the regression test for the
  k8xp incident — assert on the suggestion, not just on the warning.
- **Positive control against the whole registry**: assert the recognized set
  contains a name from each of the four sources (koanf-reflected, manual reader,
  systemconfig table, `SP_PERFORMANCE_*`). A registry that silently loses a
  source would otherwise pass every test above while spraying false positives in
  production.
- **Negative control**: a clean environment carrying only recognized names
  produces zero warnings.

`server/internal/systemconfig/systemconfig_test.go` already has registry-shaped
invariant tests over `getKnownParameters()` (e.g. `TestKnownPasswordKeys` at
[systemconfig_test.go:153](server/internal/systemconfig/systemconfig_test.go:153)
builds a `byKey` map and asserts completeness) — the closest existing precedent.

## Open questions

### `SP_ENCRYPTION_MASTER_KEY` may already be silently broken

Reasoning from the rule above: `SP_ENCRYPTION_MASTER_KEY` transforms to
`encryption.master.key`, but the tag at
[config.go:174](server/internal/config/config.go:174) is `master_key`. There is
**no manual reader** for it and **no systemconfig entry** —
`loadEncryptionMasterKey` ([app/server.go:2505](server/internal/app/server.go:2505))
reads `cfg.Encryption.MasterKey` and relies entirely on koanf. If the rule holds,
the var never binds, `cfg.Encryption.MasterKey` stays `""`, and credential
encryption silently falls back to the documented plaintext V1 path — despite the
var being documented at `web/docs/docs/configuration/security.md:27` and the
struct comment at [config.go:173](server/internal/config/config.go:173)
explicitly naming it. `SP_ENCRYPTION_MASTER_KEY_FILE` → `encryption.master.key.file`
would be worse: a map-vs-string collision with the former at the
`encryption.master.key` node — the exact failure the `applyAgentEnv` comment
([config.go:1136](server/internal/config/config.go:1136)) warns about for
`agent.keys` / `agent.keys.file`. `SP_ENCRYPTION_AUTO_MIGRATE` →
`encryption.auto.migrate` ≠ `auto_migrate` is likely broken too.

**This is unverified.** I wrote a `t.Setenv` probe to confirm it and could not
run it — `server/go.sum` currently has an unresolved merge conflict (see below).
Confirm with a ~5-line probe (`t.Setenv("SP_ENCRYPTION_MASTER_KEY", ...)`,
`Load()`, assert on `cfg.Encryption.MasterKey`) **before** acting on it.

If confirmed, this is a **security-relevant bug that deserves its own spec** —
don't fix it inline here. But it directly shapes this one: under the
"recognized = actually binds" semantic, the new check will correctly warn about a
documented variable. That's the check working as designed, and it makes a strong
acceptance criterion — run the check against a tree with `SP_ENCRYPTION_MASTER_KEY`
set and confirm it flags it. If instead you find the implementer quietly adding
the name to the registry to silence the warning, that's papering over the bug.

### `SP_BASE_URL` vs `SP_SERVER_BASE_URL`

Both are live and mean the same thing. `SP_BASE_URL` is in the systemconfig table
(→ `KeyBaseURL`) and is what the docs document
(`web/docs/docs/configuration/index.md:22`); `SP_SERVER_BASE_URL` is read
separately at [job_startup.go:184](server/internal/jobs/jobtypes/job_startup.go:184)
with the same default and is documented nowhere. Both must be in the registry, or
the check flags whichever is omitted. Worth resolving, but out of scope here.

## Note for the implementer: the tree does not currently build

`server/go.sum` is in an **unresolved merge-conflict state** (`UU`, conflict
markers at line 68) — left by a concurrent actor on this batch branch, not by
this spec. `go test ./...` fails immediately with `malformed go.sum`. Resolve or
wait for that merge before starting; **do not** work around it by editing
`go.sum` yourself.

## Implementation Plan

Status: implemented on batch/2026-07-16. The `go.sum` conflict noted above was
resolved by the coordinator (merge of origin/main + `go mod tidy`); the tree
builds cleanly and none of this work touches `go.sum`.

### Registry (union of four sources + allowlist), "recognized = actually binds"

- `config.RecognizedEnvVars()` (new `server/internal/config/envvars.go`) unions:
  - **Source 1 — koanf-reflected:** `koanfReachableEnvVars()` loads a zero
    `Config{}` through `structs.Provider(_, "koanf")` and calls `koanf.Keys()`.
    `envNameForKoanfPath` reverse-maps each leaf path to `SP_<UPPER _-joined>`,
    excluding any path with a snake_case segment (env can never reach it).
    Verified empirically that the structs provider honors `koanf:"-"`
    (`LogLevel`, `Server.Redirects`, `App.EnableBugReport` never appear) and
    emits every leaf regardless of value, so no defaults literal is needed.
  - **Source 2 — manual readers:** `manualReaderEnvVars()` enumerates every
    `SP_*` name read by the `apply*Env` helpers and the bare `os.Getenv` reads
    in `config.Load` (verified against the actual code, not just the spec list).
- `systemconfig.KnownEnvVars()` (new accessor over `getKnownParameters`) —
  **Source 3**, all 81 `EnvVar` names, pure data, no DB dependency.
- New package `server/internal/envcheck` unions the above plus:
  - **Source 4 — `SP_PERFORMANCE_*`:** the 5 resolvers in
    `job_aggregation.go` / `job_jobs_cleanup.go` (enumerated to avoid importing
    the heavy `jobtypes` package).
  - Other out-of-`config.Load` manual readers: `SP_ENTITLEMENTS_*` (saas.go),
    `SP_REGIONS` (regions_seed.go), `SP_SERVER_BASE_URL` (job_startup.go),
    `SP_LOG_HTTP_CALLS` (pkg/client).
  - Exact-name CLI allowlist: `SP_TOKEN`, `SP_SERVER`, `SP_ORG`.
  - Set-union naturally dedups deliberate double-reads.

### Matcher (`server/internal/envcheck/match.go`), two ranked rules, one suggestion

1. Segment subsequence (split on `_`, ordered subsequence either direction) —
   catches the missing/extra whole-segment class (`SP_RATE_LIMITING_TRUSTED_PROXIES`
   → `SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES`, which is edit-distance 7).
2. Hand-rolled Levenshtein ≤ 3 for character typos (`SP_LOG_LEVL` → `SP_LOG_LEVEL`).
   Rule 1 wins; within a rule, rank by closeness then lexicographically.

### Placement

`envcheck.WarnUnrecognizedEnv(ctx)` is called from `serve` in `server/main.go`,
between `setupLogger(cfg.LogLevel)` and `cfg.Validate()`. One WARN per
unrecognized `SP_*` var (with did-you-mean when matched); never fails startup.

### `SP_ENCRYPTION_MASTER_KEY` finding

Confirmed broken via a `t.Setenv` probe: with the var set, `cfg.Encryption.MasterKey`
stays `""` (and `SP_ENCRYPTION_AUTO_MIGRATE=false` leaves `AutoMigrate=true`).
Not fixed here (own spec). The new check correctly flags it (unrecognized under
"actually binds"). Both `SP_BASE_URL` and `SP_SERVER_BASE_URL` are recognized.
