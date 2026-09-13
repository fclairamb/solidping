# Contributing to SolidPing

Thanks for considering a contribution. This is a one-page on-ramp; deeper
conventions live in [`CLAUDE.md`](CLAUDE.md) (repo-wide) and the `CLAUDE.md`
files under `server/` and `web/dash0/`.

## Before you start

Search [open issues](https://github.com/fclairamb/solidping/issues) first.
For anything beyond a small fix, open an issue to discuss the approach before
writing code. Larger work at SolidPing is tracked as a spec file in
`specs/todos/` (see the naming convention in [`CLAUDE.md`](CLAUDE.md#specs));
you don't need to write one yourself, but it explains why a PR may reference
`specs/todos/YYYY-MM-DD-NN-*.md`.

## Local setup

Prerequisites: Go (version pinned in [`server/go.mod`](server/go.mod); CI runs
the version pinned in [`.github/workflows/ci.yml`](.github/workflows/ci.yml)),
[Bun](https://bun.sh) for `web/dash0` and `web/status0` (no npm/Node), and
Docker for PostgreSQL.

```bash
docker-compose up -d   # infrastructure (Postgres)
make dev                # backend + dash0 + status0, hot reload
make dev-test           # same, with SP_RUNMODE=test
```

Default credentials are in [`CLAUDE.md`](CLAUDE.md#default-credentials) — note
that the normal seeded admin (`admin@solidping.io` / `solidpass`) is forced
through a password change on first login; the test-mode user
(`test@test.com` / `test`) is not.

Database changes: add a migration, then `make migrate` (see
[`wiki/conventions/database.md`](wiki/conventions/database.md) for the
one-migration-per-release convention before adding a new file).

## Before opening a PR

```bash
make fmt && make lint && make test
```

If you touched `web/dash0`, also run its own gates from
[`web/dash0/CLAUDE.md`](web/dash0/CLAUDE.md):

```bash
make build-dash0
cd web/dash0 && bun run lint && bun run test:unit
```

Playwright E2E for the dashboard lives under `web/dash0/e2e/`
(`bun run test:e2e`).

Add a description worth a changelog entry only if the change is user-visible
— release-please writes the changelog from the PR title and body per
[`wiki/conventions/changelog.md`](wiki/conventions/changelog.md).

## PR titles are conventional commits

This repo squash-merges exclusively with the **PR title as the commit
subject**, and release-please parses that subject to build the changelog and
pick the next version. A title it can't parse is silently dropped from both.

Format: `<type>[(scope)][!]: <description>`, allowed types
`feat|fix|perf|revert|docs|style|chore|refactor|test|build|ci` (enforced by
[`.github/workflows/pr-title.yml`](.github/workflows/pr-title.yml)). One PR
per topic.

## Never name a real company — use `acme`

No real company name (employer, customer, vendor) belongs anywhere in this
repository — not in fixtures, tests, sample data, comments, or commit/PR text.
Use `acme` (`acme.com`, `acmetech`, `alice@acme.com`) instead, keeping the
shape of what you're replacing. See [`CLAUDE.md`](CLAUDE.md#never-name-a-real-company--use-acme)
for the full rule and why.

## Docs & wiki

[`web/docs/`](web/docs/) is the published documentation site
(docs.solidping.io); [`wiki/`](wiki/) is internal engineering notes. Competitor
comparisons never go in `web/docs/` — they belong in `wiki/competitors/`. The
only competitor-facing pages `web/docs/` carries are the `migrate-from-*`
import guides.

## License

By contributing, you agree your contribution is licensed under the project's
[AGPL-3.0 license](LICENSE).
