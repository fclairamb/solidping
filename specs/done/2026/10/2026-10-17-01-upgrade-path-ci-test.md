---
model: sonnet
effort: high
---

# CI never upgrades a real database from a released version

## Problem
Nothing in CI boots a previous release and then boots HEAD on the database it left behind.
Upgrades still broke twice this month:

- v0.38.4 edited migration `027_v0_39_0` after v0.38.1 had shipped it. Every database that ran
  v0.38.1 to v0.38.3 crash-looped with "migration content changed after it was applied". Fixed
  in 763c9e570 (#558) by restoring 027 and moving the new columns to `028_v0_38_5`.
- Failed or interrupted migrations were recorded as applied. Fixed in c7479ed35 (#549) with
  `migrate.WithMarkAppliedOnSuccess(true)` (`server/internal/db/postgres/postgres.go:354`).

The guards added since then are static or hand-written:

- `scripts/check-frozen-migrations.sh` (`Makefile:476`, CI step `ci.yml:193-200`) compares each
  migration file with its first tag. It catches an edited file, not a new migration that fails
  on real data, nor code that cannot read rows an older version wrote.
- `sqlite/migration_028_split_027_test.go` and its Postgres twin cover one specific upgrade.
- The `docker` job smoke test (`ci.yml:1253-1279`) boots the new image on an empty volume only,
  and only after the tag already exists.

For a monitoring tool, a broken upgrade is the fastest way to lose a user. The test should
run the way an operator upgrades: the old release fills a database, then the new binary starts
on it.

## Proposal
1. Add `scripts/upgrade-test.sh <engine> <from-tag>` (`engine` is `sqlite` or `postgres`).
   Bash, `docker`, `curl` and `jq` only, in the style of the existing `scripts/*.sh`.
   1. **Old side.** Run `ghcr.io/fclairamb/solidping:<from-tag>` (every `v*` tag has an image,
      `ci.yml:1163`) with `SP_RUNMODE=test` and an explicit `SP_DB_TYPE`. Test mode would
      otherwise pick `sqlite-memory` (`server/internal/config/config.go:2416`). Never set
      `SP_DB_RESET`.
      - SQLite: bind-mount a temp dir as the data dir (`SP_DB_DIR`).
      - Postgres: start a `postgres` container on a shared docker network and pass `SP_DB_URL`.
   2. Wait for `GET /api/mgmt/health` to answer `ok`. Test mode seeds orgs, checks, incidents,
      a status page, SLO data and screenshots (`server/test/testdata/testdata.go:25`). Then let
      the old version run its checks for 30 seconds so `results` and check state have rows the
      old code wrote.
   3. **Snapshot.** Sign in as `test@test.com` (`POST /api/v1/auth/login`) and record the counts
      that must survive: checks, incidents, status pages, connections, members. Record the
      latest result timestamp of one check.
   4. Stop the old container with `docker stop`, which sends SIGTERM like a real rolling
      upgrade.
   5. **New side.** Start the HEAD binary on the same SQLite dir or the same Postgres URL, with
      the same env, on a free port. HEAD auto-migrates at startup (`server/main.go:312`).
   6. **Assertions.** Each failure prints the last 100 lines of the new server's log.
      - Health answers `ok` within 60 seconds.
      - The log contains neither "half-migrated" (`server/internal/db/halfmigrated.go:21`) nor
        "migration content changed".
      - Every migration file in HEAD's `server/internal/db/<engine>/migrations/` is listed in
        `bun_migrations`. Read it with `sqlite3` or `psql` from a container. No endpoint
        reports migration state.
      - Every snapshot count is unchanged, read through the HEAD API with the same login.
      - Within 90 seconds, the recorded check has a result newer than the snapshot. This
        proves the scheduler runs on upgraded data, not just the HTTP layer.
      - SQLite only: when HEAD has a migration the old tag lacked, a
        `solidping.db.pre-<migration>.bak` file exists in the data dir.
   7. Clean up containers, the network and the temp dir in a `trap`, including on failure.
2. Pick the versions in the script with `--matrix`, not by hand in YAML. Print one
   `<engine> <from-tag>` per line:
   - the 3 most recent `v*` tags (`git tag -l 'v*' | sort -V`);
   - the newest tag of the previous minor (today `v0.37.0`), to cover a longer jump;
   - minus a `SKIP_TAGS` list at the top of the script, starting with `v0.38.4` (known bad).
     The 027 to 028 recovery from v0.38.4 is already covered by
     `migration_028_split_027_test.go`.
   Each tag runs on both engines: 8 runs today.
3. Add `make test-upgrade` (next to `lint-migrations`, `Makefile:476`). It builds HEAD
   (`make build`, or the server binary alone if the embedded frontend is not needed to boot)
   and runs the whole matrix locally.
4. Add an `upgrade` job to `.github/workflows/ci.yml`:
   - Same `if:` guard as `backend-postgres` (`ci.yml:232`): skip tag pushes and wiki-only PRs.
     Release-please PRs do run it: a release is exactly when an upgrade must be proven.
   - A first step runs `--matrix`; a `strategy.matrix` built from its output with `fromJSON`
     runs one leg per line, `fail-fast: false`.
   - Each leg builds HEAD's server binary, or downloads it as an artifact from the `build` job
     (`ci.yml:600`) if one is uploaded there.
   - Add `upgrade` to the `needs` of the aggregate `ci` job (`ci.yml:1149`), so a red upgrade
     blocks merges and releases.
   - Budget: each leg under 5 minutes. If the matrix makes PR CI noticeably slower, run only
     the newest tag on PRs and the full matrix in `nightly.yml`.
5. Document it in `wiki/testing/test-layers.md` (what it covers, `make test-upgrade`,
   `SKIP_TAGS`) and add one row to the Makefile targets table in the root `AGENTS.md`.

## Tests
- Positive control: proof the job can fail. On a throwaway branch, run `make test-upgrade` once
  with each sabotage below. Each run must go red on the expected assertion. Write the commands
  and outcomes in the PR description. Do not commit the sabotage.
  - (a) Edit an already-released migration, as v0.38.4 did. Expect the "content changed" log
    assertion or a health timeout.
  - (b) Add a new migration whose SQL fails on seeded data, e.g. `NOT NULL` with no default on
    a table that has rows. Expect a health timeout, and the migration must not appear in
    `bun_migrations`.
  - (c) Make HEAD delete one status page at startup. Expect the count assertion.
- The real matrix passes on both engines for every tag `--matrix` prints.
- `scripts/upgrade-test.sh --matrix` with `SKIP_TAGS` containing the newest tag returns the
  next one instead. A quick shell check is enough; no Go test.
- `make lint` stays green (shellcheck, if `make lint` runs it).

## To verify
- Which uid the image runs as, and whether the HEAD binary run by the CI user can write the
  SQLite file and dir it created. If not, run the new side in a container too: build a HEAD
  image, or mount the HEAD binary into the released image.
- Which Postgres major version production and the Helm chart use (CloudNativePG,
  `chore(helm): run on PostgreSQL through CloudNativePG`). Use the same one.
- Whether the `build` job already uploads a server binary artifact the `upgrade` legs can
  reuse instead of compiling.
- That test mode boots against a non-empty, migrated-by-another-version database without
  re-seeding or resetting it. `InitializeTestData` (`server.go:4593`) skips when org
  `00000000-...0001` exists, but check nothing else in test mode truncates tables.
- That the images of the 3 most recent tags support the runner's arch (amd64). The docker job
  publishes amd64, arm64 and riscv64.

## Non-goals
- Downgrades (HEAD then the old version).
- Upgrades from versions before v0.37.0.
- Replacing `check-frozen-migrations.sh` or the per-migration Go tests. They fail faster and
  stay.
