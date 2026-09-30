---
model: sonnet
effort: medium
---

# Show the backend test coverage as a badge in the README

## Problem
Spec 2026-09-29-06 added a backend coverage gate (`COVERAGE_MIN: "80"`, `.github/workflows/ci.yml:296`) and a filtered profile (`scripts/coverage.sh filter|total|gate`). The number is only visible in the job summary and a 14-day artifact. The README badge row (`README.md:11-15`) has Build, Release, Go, Go Reference, License, but no coverage. That spec chose "no third-party service", so the badge must be fed from our own CI.

## Proposal
1. In the `backend-postgres` job (`.github/workflows/ci.yml`, right after the "Backend coverage gate" step at ~line 293), add a step that runs only on `push` to `main`. It computes `../scripts/coverage.sh total coverage.out` and publishes a shields.io endpoint JSON (`{"schemaVersion":1,"label":"coverage","message":"81.2%","color":"..."}`).
   Colour: green at or above `COVERAGE_MIN`, yellow within 5 points below it, red otherwise.
2. Publish that JSON without a third-party service. Recommended: push it to an orphan `badges` branch of this repo (`coverage.json`), using `GITHUB_TOKEN` with `contents: write` scoped to this job only (`ci.yml:14` sets top-level `permissions`, so add a job-level override). Never run it on pull requests or forks.
3. Add the badge to `README.md` next to the Build badge (line 11 area):
   `[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/fclairamb/solidping/badges/coverage.json)](https://github.com/fclairamb/solidping/actions/workflows/ci.yml)`
4. Add a short note to `wiki/` (where spec 06 documented the coverage layer, find it with `grep -rn COVERAGE_MIN wiki`) saying how the badge is produced and that the `badges` branch is machine-written.
5. Add a `scripts/coverage.sh badge <profile>` subcommand that prints the endpoint JSON, so the colour logic is testable outside CI. Extend `scripts/coverage_test.sh` (already run in CI at `ci.yml:200`).

## Tests
- `scripts/coverage_test.sh`: `badge` prints valid JSON with `schemaVersion` 1 and the total, for a profile at 85% (green), 78% (yellow) and 60% (red).
- `scripts/coverage_test.sh`: `badge` on a missing or empty profile exits non-zero and prints no JSON (negative case: a broken run must not publish a bogus badge).
- CI: after merge to `main`, `curl` the raw `coverage.json` URL and check it parses. A PR run must not touch the `badges` branch.

## To verify
- Whether `backend-postgres` already runs on `push` to `main` or is gated behind a label or path filter (`ci.yml:3-8`, job `if:`). If it is skipped on some pushes, the badge only refreshes when it runs. Say so in the wiki note.
- Whether branch protection or a ruleset blocks `GITHUB_TOKEN` pushing to a `badges` branch.

## Open questions
- Which number should the badge show? Recommended: the backend total only (the gated one). dash0 and status0 coverage is report-only (`ci.yml:396`, `ci.yml:463`) and would be misleading mixed in.

## Resolved open questions
- The badge shows the backend total only (the gated number, `scripts/coverage.sh total`). Never mix dash0 or status0 coverage into it.
