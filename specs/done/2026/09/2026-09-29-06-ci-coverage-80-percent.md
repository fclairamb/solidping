---
model: sonnet
effort: high
---

# CI measures no test coverage: add it and reach 80% on the backend

## Problem
Nothing in CI measures coverage. `grep` for `cover`, `codecov` and `coveralls` across `.github/`, `Makefile` and `web/dash0/` finds nothing. The Go job runs `go test -count=1 ./... -short` (`.github/workflows/ci.yml:190`), the Postgres job runs `go test ... -p 1 -v ./...` (`ci.yml:271`), and `make test` is `go test ./... -short` (`Makefile:359`). dash0 runs `vitest run` (`ci.yml:359`, `web/dash0/vitest.config.ts`) with no coverage provider configured. So today's percentage is unknown and there is nothing to stop it from dropping.

The goal is to publish coverage from CI and get the backend to 80%.

## Proposal
1. Measure first. Run `make test-postgres`-equivalent with `-coverprofile=coverage.out -coverpkg=./...` and record per-package and total numbers in `wiki/testing/coverage.md` (new, linked from `wiki/README.md`). If the total is already >= 80%, skip step 4.
2. Add `make test-cover` in `Makefile` (next to `test`, line 357). It runs the backend tests with `-coverprofile` and prints `go tool cover -func` totals. Exclude generated code (`pkg/client` oapi output, `internal/checkers/schemas` output, mocks) from the profile with a filter step, so the number reflects hand-written code.
3. Wire it into CI:
   - `backend-postgres` (`ci.yml:201`) is the layer that runs every suite, so it produces the profile. Add `-coverprofile` there and upload `coverage.out` as an artifact.
   - Add a step that prints the total to `$GITHUB_STEP_SUMMARY`, and fails the job when the total is below the threshold (a `COVERAGE_MIN` env var, default 80 once the target is reached).
   - Do not add a third-party service (Codecov etc.) unless the user asks: the artifact plus the step summary is enough.
4. Raise coverage to 80%. Take the lowest-covered packages from step 1 by uncovered statements, largest first, and add table-driven tests (see `server/CLAUDE.md`). Prefer real logic (handlers, services, checkers) over trivial getters. Land the CI gate at the last measured value first, then ratchet it up in the same PR series until it reaches 80.
5. Frontend: measure, don't gate. The 80% target and the gate apply to the backend only.
   - dash0: add `@vitest/coverage-v8` and a `test:unit:cover` script in `web/dash0/package.json`. Add a `coverage` block to `web/dash0/vitest.config.ts` (reporters `text-summary` and `json-summary`). Print the total in the `dash0` job summary (`ci.yml:359`) and upload the report as an artifact.
   - status0: its unit step is `bun test ./src` (`ci.yml:415`). Use `bun test --coverage ./src` and print the result in the job summary.
   - docs: the site has its own `bun run test:unit` (`ci.yml:450`). Leave it out unless it is a one-line change.
6. Document the target, the ratchet rule, and that frontend coverage is report-only, in `wiki/testing/test-layers.md`.

## Tests
- Frontend: `bun run test:unit:cover` in dash0 and `bun test --coverage ./src` in status0 both exit 0 and print a total. There is no threshold, so no negative case; a broken test must still fail the job.
- `make test-cover`: produces `coverage.out` and a total, and excludes the generated packages (assert `pkg/client` paths are absent from the filtered profile).
- CI gate, positive: with `COVERAGE_MIN` below the total, the step passes.
- CI gate, negative: with `COVERAGE_MIN` above the total (run locally with `COVERAGE_MIN=101`), the step exits non-zero and prints the total and the threshold.
- New unit tests added in step 4 go in the packages they cover, each with at least one failure-path case.
- `backend-postgres` must stay under its time budget; if `-coverpkg=./...` slows it noticeably, note the delta in the wiki page.

## To verify
- The current backend total (step 1). This decides how much of step 4 is needed.
- Whether `bun test --coverage` in status0 prints a usable table under the pinned Bun version.
- Whether coverage is meaningful under `-short` alone (SQLite only) or needs the Postgres layer. Postgres-only code paths are skipped by `-short`, so the Postgres job is the recommended source.
- The exact list of generated paths to exclude.
