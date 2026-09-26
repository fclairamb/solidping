---
model: sonnet
effort: medium
---

# envcheck warns "Unrecognized SP_* environment variable" for variables that are actually read

## Problem

The boot-time check `internal/envcheck` (called from `main.go` via
`envcheck.WarnUnrecognizedEnv`) logs `Unrecognized SP_* environment variable is
ignored` for two variables that the server does read. Seen on the prod API at
v0.32.1:

- `SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`: const
  `envEntitlementsUpgradeTokenSecret` in `server/internal/app/saas.go:27`, read
  with `os.LookupEnv` and seeded into the `entitlements.billing_upgrade_token_secret`
  parameter. The warning also says `didYouMean=SP_TOKEN` (a CLI var from
  `cliAllowlist()`). An operator following that hint could rename or remove a
  security secret that is in use, which silently re-enables the bearer fallback
  for minting upgrade tokens (see the SaaS section of the root `CLAUDE.md`).
- `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`: const `envSystemAgentEnrollmentTokens` in
  `server/internal/app/systemagents.go:21`, read with `os.LookupEnv` at
  `systemagents.go:62`.

The allowlist is `recognizedEnvVars()` in `server/internal/envcheck/envcheck.go:122`.
It already unions `otherManualReaderEnvVars()` (`envcheck.go:159`), a hand-kept
list of SP_* names read directly outside `config.Load` (koanf collapses
underscores in multi-word keys, so these readers exist on purpose). Both new
names were added to `internal/app` without updating that list. Nothing catches
the drift: the list is literal strings, and no test compares it against the
code that actually reads env vars.

## Proposal

### 1. Close the known gaps

Add to `otherManualReaderEnvVars()`, and update its doc comment to mention
`internal/app/systemagents.go`:

- `SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`
- `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`

Then sweep for any other manual reader the allowlist misses. Grep non-test Go
files under `server/` for `os.LookupEnv("SP_`, `os.Getenv("SP_` and string
constants whose whole value is an `SP_*` name, and compare each against what
`recognizedEnvVars()` returns. Known readers already covered, for reference:
`internal/app/saas.go` (the other seven entitlements vars),
`internal/app/regions_seed.go` (`SP_REGIONS`),
`internal/jobs/jobtypes/job_startup.go:335,366` (`SP_RUN_MODE`,
`SP_SERVER_BASE_URL`), `pkg/client/client.go:308` (`SP_LOG_HTTP_CALLS`), and
everything in `internal/config/` (covered by `config.RecognizedEnvVars()` via
`manualReaderEnvVars()` in `internal/config/envvars.go`).

### 2. Regression test

Referencing the owning constants directly is not an option: they are
unexported (`internal/app`), and importing `internal/app` or
`internal/jobs/jobtypes` from `envcheck` pulls in exactly the heavy graph the
existing comments (`envcheck.go:139-142`, `envcheck.go:154-158`) say we avoid.
So the test scans the source tree.

Add a test in `internal/envcheck` (new file, e.g. `sourcescan_test.go`), using
`testify/require` and `t.Parallel()`:

- Walk the `server/` module root (locate it from the test's working dir, e.g.
  `../..`, and assert `go.mod` is there so a moved package fails loudly rather
  than scanning nothing).
- Parse each non-`_test.go` `.go` file with `go/parser` and inspect every
  `*ast.BasicLit` of kind `STRING` whose unquoted value matches
  `^SP_[A-Z0-9_]+$` exactly. Whole-literal matching picks up constants such as
  `envEntitlementsUpgradeTokenSecret` and inline `os.Getenv("SP_...")` calls,
  and naturally skips error and log messages that merely mention a variable
  (`"set SP_ENCRYPTION_MASTER_KEY first"`), and the bare `"SP_"` koanf prefix.
- Assert every collected name is in `recognizedEnvVars()`. The failure message
  must list each missing name with its `file:line` and say to add it to
  `otherManualReaderEnvVars()` (or to the right source).
- Skip directories that are not server env readers, with a comment on each
  exclusion: `internal/envcheck` itself, `cmd/membench` (it sets env for child
  processes; those names are recognized anyway, so excluding is optional),
  vendored/third-party dirs if any. Keep an explicit, commented exception set
  for names that are legitimately not server config, e.g.
  `SP_TEST_REQUIRE_POSTGRES` (`internal/testsupport/postgres.go:34`, test
  harness only). Do not add it to the runtime allowlist.
- Positive control: assert the scan found `SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`
  and `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`, so a broken walker (wrong root,
  zero files parsed) cannot pass vacuously. Verify the test fails before the
  step 1 fix and passes after.

Also add a table case to `envcheck_test.go` asserting `checkEnviron` produces no
warning for the two names from step 1.

### 3. Verify

From `server/`: `go test ./internal/envcheck/...` and `make lint-back`. Fix lint
findings in code, never relax `.golangci.yml`.

Commit with a `fix:` conventional message (e.g.
`fix(envcheck): recognize manually read entitlements and system-agent env vars`).

## Open questions

- If the sweep finds a reader whose name is also a koanf-reachable path under
  a different spelling, prefer fixing the allowlist over renaming the variable;
  renaming a deployed env var is a breaking change for operators.
