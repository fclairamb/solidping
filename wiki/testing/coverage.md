# Test coverage

Spec `2026-09-29-06`. Backend coverage is measured in CI, gated at 80%, and
published as an artifact plus a job-summary line. Frontend coverage is
report-only.

## How it is measured

- `make test-cover` writes `server/coverage.out` and prints the total. It runs
  `go test -short -coverprofile -coverpkg=./...` (SQLite only, ~8 min).
  `make test-cover COVER_FULL=1` runs the Postgres layer instead (what CI
  does, ~35 min locally with `-coverpkg=./...`).
- `-coverpkg=./...` credits a package for lines exercised by *other*
  packages' tests (handlers hit through the app, for example). Duplicate
  blocks across test binaries are merged by `go tool cover`.
- `scripts/coverage.sh` filters the profile, prints the total, and gates on
  `COVERAGE_MIN`. `scripts/coverage_test.sh` is its self-test (CI runs it in
  `backend-lint`).
- Excluded from the profile: `pkg/client` (oapi-codegen output),
  `third_party/` (vendored grdp fork), `*_generated.go`, `mock_*.go`,
  `*_mock.go`. `internal/checkers/schemas` holds JSON, no Go output, so
  nothing to exclude there.

## Where the source of truth is

`-short` skips every Postgres suite, so the `backend-postgres` job is the
source: it produces the profile, uploads it as the `backend-coverage`
artifact, writes the total to the step summary and fails below `COVERAGE_MIN`
(`ci.yml`, step "Backend coverage gate").

## Baseline (2026-09-29, hand-written code only)

| Run | Total |
|---|---|
| `-short` (SQLite only), raw, generated code included | 63.3% |
| `-short`, generated + vendored code excluded | 75.8% |
| Postgres layer (`COVER_FULL=1`), excluded, before step 4 tests | 80.4% |

Largest uncovered areas at that point (statements): `pkg/cli` ~3500,
`internal/db/postgres` ~2000, `internal/handlers/auth` ~1600,
`internal/jobs/jobtypes` ~1100, `internal/integrations/slack` ~900.

The only test added by this spec is `pkg/cli/commands_smoke_test.go`, which
drives every CLI leaf command against a fake server (success, populated
lists, all-flags, 404/500 paths); `pkg/cli` went from ~7% to 60%.

CI time: the `backend-postgres` job now runs with `-coverpkg=./...`, which
makes every test binary instrument every package. A local full run took
~34 minutes (no like-for-like baseline was measured on the same machine).
Check the first CI run against the 45 minute budget and note the delta here.

## The ratchet

`COVERAGE_MIN` in `ci.yml` starts at 80. When a change raises the total,
raise `COVERAGE_MIN` to the last measured value (rounded down); never lower
it to make a PR pass. Add tests instead.

## Frontend (report-only)

- dash0: `bun run test:unit:cover` (vitest, `@vitest/coverage-v8`,
  `text-summary` + `json-summary`). About 21% statements on 2026-09-29.
- status0: `bun test --coverage ./src` (Bun 1.3.4 prints a per-file table).
- docs: not measured.

No threshold, no gate. A broken test still fails the job.
