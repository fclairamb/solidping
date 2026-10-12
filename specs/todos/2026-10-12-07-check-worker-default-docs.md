---
model: sonnet
effort: medium
---

# The worker settings bind from the environment, their documented defaults match the code, and a test keeps them matched

## Problem
The report: the public docs say `SP_SERVER_CHECK_WORKER_NB` defaults to `3`, the code default is `25`. That is true. Checking it found a worse bug underneath: **setting `SP_SERVER_CHECK_WORKER_NB` or `SP_SERVER_JOB_WORKER_NB` has no effect at all.**

**The code default.** `CheckWorkerConfig.Nb` (`server/internal/config/config.go:1643-1648`, tag `koanf:"check_worker"` at `:1733`) defaults to `25` at `config.go:1950`. The comment at `:1942-1949` gives the reason (spec 2026-06-30-09): check execution is network I/O, a small pool manufactures head-of-line blocking, and slow checks are deprioritized rather than capped. `newCheckWorker` falls back to `25` too when the value is `<= 0` (`server/internal/checkworker/worker.go:323-330`), although `startCheckWorker` returns before that on `<= 0` (`server/internal/app/server.go:4078-4082`). `JobWorkerConfig.Nb` defaults to `2` (`config.go:1938`). The dashboard performance page agrees with the code: `useState("25")` and `useState("2")` (`web/dash0/src/routes/orgs/$org/server.performance.tsx:45-46`). The history is squashed: `3` was the default from the first commit, `25` arrived in the 2026-07-03 batch (`73ff04718`). The docs were never updated.

**Why 25 is the right default, small hosts included.**
- Idle cost is negligible. Spec 2026-10-12-04 measured 61-66 goroutines using 3 MB of stack in total with 25 check runners (`specs/todos/2026-10-12-04-reduce-idle-memory-footprint.md:27,65`).
- It matches the DB pool: `dbPoolMaxOpenConnsDefault = 25` (`config.go:44`).
- `3` would break another default. `FastLaneReserved` defaults to `5` (`config.go:1980`) and is clamped to `pool − 1` (`checkworker/worker.go:199,343`). With 3 runners the fast lane reservation is clamped to 2 and logs a warning on every start.
- Browser checks, the only memory-heavy kind, are capped separately at 4 per worker (`web/docs/docs/installation/kubernetes.md:383`).

So the docs change, not the code.

**The env var does nothing (verified).** `SP_SERVER_CHECK_WORKER_NB` cannot reach the struct through koanf: the env provider turns every `_` into `.` (`config.go:2184-2199`), giving `server.check.worker.nb`, while the field lives at `server.check_worker.nb`. There is no manual reader in `config.Load` for it (there is one for `SP_SCHEDULING_FAST_LANE_RESERVED`, `config.go:2806`). The only binding is the system-parameter table: `KeyCheckWorkers` / `KeyJobWorkers` (`server/internal/systemconfig/systemconfig.go:30-31`), with `EnvVar` set at `:327` and `:339`. Their `ApplyFunc` accepts only `float64` or `int` (`:329-335`, `:341-347`). The overlay passes the raw env **string** (`systemconfig.go:1530-1533`), so the type switch matches nothing. A probe test against `origin/main` confirmed it: with `SP_SERVER_CHECK_WORKER_NB=8` and `SP_SERVER_JOB_WORKER_NB=4`, `config.Load()` yields `25` and `2`, and `ApplyFunc(cfg, "8")` leaves them at `25` and `2`.

It is worse than a no-op:
- The overlay `continue`s after the env branch (`systemconfig.go:1533`), so a value saved from the dashboard performance page is skipped too while the env var is set.
- `EnvOverriddenKeys` (`systemconfig.go:268-280`, served at `server/internal/handlers/system/service.go:263`) reports `server.check_workers` as env-overridden, so the UI tells the operator the env var is in charge.
- `envcheck` treats the name as recognized because it unions `systemconfig.KnownEnvVars()` (`server/internal/envcheck/envcheck.go:117,130-131`). Its own rule is "recognized = actually binds". These two names break it, silently.

A second probe applied candidate string values to every `getKnownParameters()` entry whose env var `config.Load` does not bind (56 of 96). Only these two ignore every string. The other int/bool `ApplyFunc`s that also ignore strings (`SP_EMAIL_PORT`, `SP_EMAIL_ENABLED`, `SP_EMAIL_INSECURESKIPVERIFY`, `SP_APP_FEEDBACK_MAX_STORAGE_BYTES`, `SP_SCHEDULING_FAST_LANE_RESERVED`) are bound by `config.Load` first, so env still wins for them.

**Every place that states a default or a value**

| Place | Setting | States | Code | Status |
|---|---|---|---|---|
| `web/docs/docs/configuration/index.md:58` | `SP_SERVER_JOB_WORKER_NB` | `2` | `2` | correct, but the var does not bind |
| `web/docs/docs/configuration/index.md:59` | `SP_SERVER_CHECK_WORKER_NB` | `3` | `25` | **wrong**, and does not bind |
| `web/docs/docs/configuration/index.md:362-363` | env example | `4` / `8` | n/a | example, has no effect today |
| `web/docs/docs/configuration/index.md:386-387` | YAML example | `job_worker_nb: 4`, `check_worker_nb: 8` under `server:` | key is `server.job_worker.nb` / `server.check_worker.nb` (`config.go:1626,1646,1732-1733`) | **wrong keys**, silently ignored |
| `wiki/conventions/runners.md:141` | `SP_SERVER_CHECK_WORKER_NB` | `3` | `25` | **wrong** |
| `wiki/conventions/runners.md:142` | `SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD` | `5m` | `5m` (`config.go:1941`) | **unbound**: no reader, `envcheck` warns on it |
| `wiki/conventions/runners.md:161` | check execution timeout | `30s` | `CheckTimeoutMs: 15000` (`config.go:1964`) | **wrong** |
| `wiki/conventions/runners.md:219` | `SP_SERVER_JOB_WORKER_NB` | `2` | `2` | correct, but does not bind |
| `wiki/conventions/runners.md:220` | `SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD` | `5m` | field exists (`config.go:1625,1937`) but nothing reads `JobWorker.FetchMaxAhead`; only the check worker uses its own (`checkworker/worker.go:657`) | **unbound and unused** |
| `docker-compose.yml:54`, `docker-compose.build.yml:38` | `SP_SERVER_JOB_RUNNERS=1` | `1` | name does not exist | **stale name**, `envcheck` warns |
| `server/test_region.sh:18-19`, `server/test_checkrunner.sh:60` | `SP_SERVER_CHECK_RUNNERS=1`, `SP_SERVER_JOB_RUNNERS=0` | | names do not exist | **stale names** |
| `web/dash0/.../server.performance.tsx:45-46` | UI defaults | `25` / `2` | `25` / `2` | correct |
| `specs/todos/2026-10-12-04-reduce-idle-memory-footprint.md:90,94` | advice + drift note | "lower it on small hosts", `config.go:1887` | var does not bind; the default is now at `config.go:1950` | see below |

Places checked with nothing to fix: `deploy/helm/solidping/values.yaml` (only `extraEnv: {}` at `:28-30`, no worker key), `deploy/casaos/solidping/docker-compose.yml`, `deploy/coolify/solidping.yaml`, `deploy/fly/`, `.env.example`, `README.md` (its config table, which also said `3`, was cut in `bed9a99d8` on 2026-09-05). There is no notification-worker pool setting: notifications run as jobs on the job runners.

**`SP_RUNTIME_*`** are documented only in `wiki/runbooks/memory-profiling.md:751-766`. Spec 2026-10-12-04 step 5 already adds them to the public docs. This spec does not.

**Overlap with spec 2026-10-12-04.** Its step 5 last bullet fixes the `3` in `index.md:59` and `runners.md:141`. It is older, so `/implement-todos` will probably land it first. Its step 5 also tells small-host operators to lower `SP_SERVER_CHECK_WORKER_NB`, which does nothing until step 1 below lands. This spec owns the binding and the drift test. If spec 04 already changed the two rows, leave them and let the test confirm.

**Why it recurs.** The docs tables are hand-written and nothing compares them with the code. The repo already has the pattern for this (`server/internal/checkers/registry/docs_anchor_test.go:27-34` resolves the repo root and reads `web/docs`, `server/internal/notifications/settings_drift_test.go` checks drift), just not for config defaults.

## Proposal
1. **Make the two env vars bind.** Follow the `SP_SCHEDULING_FAST_LANE_RESERVED` precedent, both halves:
   - In `config.Load`, add a manual reader next to `applySchedulingEnv` (`config.go:~2806`), e.g. `applyWorkerPoolEnv(cfg *Config)`, that parses `SP_SERVER_CHECK_WORKER_NB` and `SP_SERVER_JOB_WORKER_NB` with `strconv.Atoi` into `cfg.Server.CheckWorker.Nb` / `cfg.Server.JobWorker.Nb`. An unparseable value is logged at WARN and ignored. Register both names in `manualReaderServerEnvVars` (`server/internal/config/envvars.go`, starts at `:150`).
   - In `systemconfig.go:325-348`, make both `ApplyFunc`s also accept a `string` (`strconv.Atoi`), so the env > DB > default overlay at `:1530` applies the env value instead of dropping it. Extract a small `applyIntParam(dst *int, value any)` helper if a third copy appears.
   - `0` keeps its meaning (runners disabled, `server.go:4019,4079`). Negative values: keep today's behaviour (`<= 0` disables).
2. **Bind or drop the fetch-ahead vars.** Add `SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD` to the same manual reader (`time.ParseDuration` into `cfg.Server.CheckWorker.FetchMaxAhead`) and register it. The wiki explains it at length (`runners.md:202-207`) and the check worker uses it. Remove the `SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD` row from `runners.md:220`: nothing reads `JobWorker.FetchMaxAhead`. Leave the struct field alone (removing it is out of scope).
3. **Fix the docs.**
   - `web/docs/docs/configuration/index.md:59`: default `25`. Reword the description to say these are the maximum concurrent checks / jobs, that idle runners cost a few KB each, and that the value can also be set from the dashboard (Server, Performance) with the env var taking precedence.
   - `index.md:386-387`: replace `job_worker_nb` / `check_worker_nb` with the real nesting:
     ```yaml
     server:
       job_worker:
         nb: 4
       check_worker:
         nb: 8
     ```
   - `wiki/conventions/runners.md:141`: `25`. `:161`: the default check timeout is 15s (`SP_SCHEDULING_CHECK_TIMEOUT_MS`, `config.go:1964`). Add one line under "How many runners do I need?" with why the default is 25: idle cost (3 MB for all goroutines, spec 04), the DB pool of 25, and the fast-lane reservation of 5 that needs a pool larger than 5.
   - `docker-compose.yml:54`, `docker-compose.build.yml:38`: `SP_SERVER_JOB_WORKER_NB=1`.
   - `server/test_region.sh:18-19`, `server/test_checkrunner.sh:60`: `SP_SERVER_CHECK_WORKER_NB=1`, `SP_SERVER_JOB_WORKER_NB=0`.
4. **Prevent recurrence with a test, not a generator.** Add `server/internal/envcheck/docs_defaults_test.go` (package `envcheck`, which already sees both `config` and `systemconfig`). It reads every `web/docs/docs/configuration/*.md` plus `wiki/conventions/runners.md`, resolving the repo root the way `docs_anchor_test.go:27-34` does. For each markdown table whose header has a `Default` column and whose first cell starts with a backticked `SP_` name:
   - **Every documented name must be recognized** by the same union `envcheck.Check` uses (`envcheck.go:116-131`). Today this catches the two fetch-ahead rows. (`SP_REGIONS` at `index.md:99` was also reported unrecognized by the probe: check it, see To verify.)
   - **A literal default must match the code.** A literal is a cell that is exactly one backticked token (about 55 rows today). Prose cells (`-`, `derived`, `hostname`, `auto-generated`, `driver default`, text with links) are skipped. The code value comes from `config.Load()` run with no `SP_*` variable set, read as:
     - koanf-reachable names: reflect the loaded config through `structs.Provider(cfg, "koanf")` and map path to name with the existing `envNameForKoanfPath` logic (`config/envvars.go`; export a helper if needed, e.g. `config.DefaultsByEnvVar()`).
     - every other name: an explicit `map[string]func(*config.Config) string` in the test, e.g. `"SP_SERVER_CHECK_WORKER_NB": func(c) string { return strconv.Itoa(c.Server.CheckWorker.Nb) }`, and for derived defaults the resolved value (e.g. `SP_POSTHOG_HOST` → `config.DefaultPostHogHost`, `config.go:447`).
     - A literal row with neither a koanf path nor an accessor **fails** with "add an accessor for SP_X in docs_defaults_test.go". That is what makes it robust: a new documented default cannot slip past unchecked.
   - Compare after normalizing: durations through `time.ParseDuration` on both sides (`5m` equals `5m0s`), bools and ints by value.
   - The failure message names `file:line`, the documented value and the code value.

   Why a test and not `make docs-config`: the tables are hand-written by topic with prose, links and admonitions, spread over 15 pages. About 77 of 118 documented rows bind through manual readers or the system-parameter table, not struct tags, so a struct-tag generator would cover less than half and still need a description registry for every key, plus a rewrite of the pages. The test is one file, runs in the normal `go test ./...`, follows two existing drift tests, and fails exactly where the drift is.
5. **Guard the binding itself.** Add a test in `server/internal/systemconfig/systemconfig_test.go` (`TestEveryParameterEnvVarTakesEffect`): for every `getKnownParameters()` entry whose `EnvVar` is not in `config.RecognizedEnvVars()` (so the overlay is its only binding), applying at least one well-formed string (`"7"`, `"true"`, `"1h"`, `"x@acme.example"`, `"https://acme.example"`, `"abc"`) to a zero `config.Config` must change it. This is the probe that found the bug, and it holds for the other 54 entries today.

Backward compatibility: no key is renamed and no default changes. Deployments that set either env var get the value they asked for, where today they silently run with 25 / 2. That is a behaviour change only for those who set it. Call it out in the PR and the changelog entry ("`SP_SERVER_CHECK_WORKER_NB` and `SP_SERVER_JOB_WORKER_NB` were ignored, they now apply"). Someone who set `SP_SERVER_CHECK_WORKER_NB=3` from the old docs drops from 25 to 3 runners after upgrading, and the fast-lane clamp warns. Mention that in the changelog line too. A DB value saved from the UI keeps working, and is still overridden by the env var, as the UI already claims.

## Tests
Table-driven, per `server/AGENTS.md`.
- `server/internal/config/envvars_test.go`, extend `TestManualReaderEnvVarsBind` (`:88`) or add `TestWorkerPoolEnvBinds`:
  - `SP_SERVER_CHECK_WORKER_NB=8` → `Nb == 8`; `SP_SERVER_JOB_WORKER_NB=4` → `4`.
  - `0` → `0` (runners disabled).
  - `abc` → default kept (`25` / `2`), no error from `Load`.
  - unset → `25` / `2`.
  - `SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD=2m` → `2*time.Minute`; `bogus` → `5m`.
- `server/internal/systemconfig/systemconfig_test.go`:
  - `ApplyFunc` for `KeyCheckWorkers` and `KeyJobWorkers` with `"8"`, `float64(8)`, `int(8)` all set 8. `"abc"` leaves the value unchanged.
  - Overlay precedence: env `"8"` + DB `10` → 8. No env + DB `10` → 10. No env, no DB → 25. Use the same test setup as the existing overlay tests.
  - `TestEveryParameterEnvVarTakesEffect` (Proposal step 5).
- `server/internal/envcheck/docs_defaults_test.go`:
  - The real docs pass (after step 3).
  - Positive control, as `TestSourceScanFindsKnownGapsPositiveControl` (`server/internal/envcheck/sourcescan_test.go:207`) does: run the parser and comparator on an in-memory markdown table with `` | `SP_SERVER_CHECK_WORKER_NB` | `3` | ... | `` and assert it reports a mismatch naming `3` and `25`. Same for an unrecognized name (`SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD`) and for a literal row with no accessor.
  - Normalization cases: `5m` vs `5m0s` equal, `30s` vs `15s` not, `true` vs `false` not.
- `server/internal/envcheck/envcheck_test.go`: `SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD` set → no warning (it now binds).

No E2E: no UI change.

## To verify
- That the edge worker and agent modes (`SP_NODE_ROLE=checks` / `agent`, HTTP backend) read `cfg.Server.CheckWorker.Nb` from `config.Load` and never run `systemconfig.Initialize`. If so, step 1's `config.Load` reader is the only thing that makes the env var work there, and the systemconfig half alone would not be enough.
- `SP_REGIONS` (`web/docs/docs/configuration/index.md:99`): the probe found it in neither `config.RecognizedEnvVars()` nor `systemconfig.KnownEnvVars()`. Check whether it is read somewhere the registry misses (then register it) or is dead (then remove the row). The docs test will force a decision either way.
- The derived defaults the probe flagged, before writing their accessors: `SP_POSTHOG_HOST` (`index.md:310` says `https://eu.i.posthog.com`, the struct is empty, `config.DefaultPostHogHost` exists), `SP_SMS_PROVIDER` (`sms.md:76` says `twilio`, the struct is empty), `SP_NODE_REGION` (`runners.md:143` says `default`, resolved at `config.go:2242-2243`). Use the resolved value, not the struct zero.
- Whether `analytics.md:50` (`–` "first-party proxy") and `index.md:310` (`https://eu.i.posthog.com`) disagree about the same default. If they do, fix the wrong one; the test only compares literals.
- That the YAML nesting in step 3 actually loads: a test or a manual `config.yml` with `server.check_worker.nb: 8` gives 8 through `loadConfigFiles` (`config.go:1154-1175`).
- Whether spec 2026-10-12-04 has landed when this one is implemented, and if so that its two row changes are what step 3 expects. Do not duplicate its `SP_RUNTIME_*` docs work.

## Acceptance Criteria
- [ ] `SP_SERVER_CHECK_WORKER_NB` and `SP_SERVER_JOB_WORKER_NB` take effect from the environment, with and without the system-parameter overlay, and a UI-saved value is used when they are unset.
- [ ] `SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD` binds; the job-worker fetch-ahead row is gone from the wiki.
- [ ] Every place in the table above says `25` for the check pool, uses the real YAML keys and the real env names.
- [ ] `docs_defaults_test.go` passes on the fixed docs and fails on a `3` in the fixture.
- [ ] `TestEveryParameterEnvVarTakesEffect` passes.
- [ ] The changelog entry says the two variables were ignored before and apply now.
