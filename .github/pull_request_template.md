<!-- Title must be a conventional commit: feat|fix|perf|revert|docs|style|chore|refactor|test|build|ci
     e.g. "fix(checks): honour HTTP cookie jar across redirects" — it becomes the changelog line. -->

## What & why

## How to test

## Checklist
- [ ] `make fmt && make lint && make test` pass locally (`make build-dash0 && cd web/dash0 && bun run lint && bun run test:unit` if `web/dash0` changed)
- [ ] Tests added or updated for the change
- [ ] Docs updated (`web/docs/`) if user-visible; wiki (`wiki/`) if operational
- [ ] Schema changes: appended a `SECTION:` to the current *unreleased* migration file under `server/internal/db/postgres/migrations/` and `server/internal/db/sqlite/migrations/` (one consolidated migration per release, per engine — see `wiki/conventions/database.md`), not a new numbered file
