# AGENTS.md

Agent instructions for every coding agent (Claude Code, Codex, Cursor, Copilot, Gemini CLI). Each `CLAUDE.md` in the repo is a one-line `@AGENTS.md` stub so Claude Code loads the same file. Edit `AGENTS.md`, never the stub.

## Core technologies
- **Backend**: Go 1.24+ (see `server/AGENTS.md` for details)
- **Dashboard**: React + TanStack Router (see `web/dash0/AGENTS.md` for details)
- **Infrastructure**: Docker Compose with PostgreSQL for monitoring data storage
- **Monitoring**: Multi-protocol ping/health checking with distributed worker system
- **Docs site**: Docusaurus in `web/docs/` (baseUrl `/docs/`), embedded in the Go binary.
  - Served at `/docs` on every host (like `/d`, `/s`). `docs.solidping.io` redirects its root into `/docs` (`server.docs_host` / `SP_DOCS_HOST`).
  - The API reference is generated at build from `server/internal/app/openapi/openapi.yaml`. The OpenAPI explorer is at `/openapi`.
  - `/docs/changelog` is generated at build from the root `CHANGELOG.md` (entry conventions: `wiki/conventions/changelog.md`).
  - `llms.txt` / `llms-full.txt` come from `docusaurus-plugin-llms`. They are served at `/docs/` and at the root path `/` (same embedded file).
  - The marketing site (`www.solidping.io`) is the separate `solidping-website` repo. Internal engineering notes live in `wiki/`.
  - Never put competitor comparisons in the docs site. They go in `wiki/competitors/` (one `{name}.md` each, indexed in `wiki/README.md`).
  - The only competitor-facing pages in `web/docs/` are the `migrate-from-*.md` import guides.

## Development workflow
Laptop only: if a server already runs on port 4000, apply code changes directly (`make dev` / `make dev-test` hot-reload). `rtk` prefixes, the k8xp VPN and `gopass` are user-global setup, not part of the repo. Sandboxes and Claude cloud sessions: see [Without Docker](#without-docker-cloud-sessions-ci-like-sandboxes).

1. Start infrastructure: `docker-compose up -d`
2. Run everything: `make dev` (backend + dash0 + status0 with hot reload)
2b. Local env: `make dev` sources `dev.priv.env` (git-ignored, shell `KEY=value` lines) when it exists, e.g. the `SP_AI_*` variables. A git worktree without one uses the main checkout's. Read secrets there with `$(gopass show -o <path>)`.
3. Test mode: `make dev-test` (same but with `SP_RUNMODE=test`)
4. Database changes: add migrations, then `make migrate`

Dev logs live in `logs/*.log` (`backend.log`, `dash0.log`, `status0.log`), size-rotated with `.1`/`.2` suffixes (~20 MB cap); all three processes run as children of the `server/cmd/devloop` supervisor, so Ctrl-C stops everything.

### Without Docker (cloud sessions, CI-like sandboxes)
No Docker or Postgres is needed. SQLite is the default database. Full runbook: [wiki/runbooks/claude-cloud.md](wiki/runbooks/claude-cloud.md).

- Once per fresh sandbox: `scripts/cloud-setup.sh` (verifies Go, installs bun and golangci-lint into `~/.local/bin`, `make deps`, builds the frontend).
- Smoke-test server (run it in the background, then `curl localhost:4000/api/mgmt/version`): `cd server && SP_RUNMODE=test SP_DB_TYPE=sqlite SP_DB_DIR=$(mktemp -d) go run . serve`. Test-mode login is `test@test.com` / `test` / org `test`.
- Before pushing from a sandbox run `make lint` and `make test`. Leave `make test-postgres`, `make test-slow` and the full Playwright suite to CI.

### Key Makefile targets
| Target | Purpose |
|---|---|
| `make build` | Build everything |
| `make dev` | Hot-reload backend + dash0 + status0 |
| `make dev-test` | Same, with `SP_RUNMODE=test` |
| `make dev-saas` | Same, in SaaS mode — pairs with `../solidping-billing` `make dev` (see SaaS mode below) |
| `make test` | Run backend tests (`-short`: SQLite only, skips every Postgres suite) (`wiki/testing/test-layers.md`) |
| `make test-postgres` | Run the Postgres test layer (non-short, `-p 1`, `SP_TEST_REQUIRE_POSTGRES=1`) — exactly what the `backend-postgres` CI job runs, ~12 min |
| `make test-slow` | Run the `slowtests` build-tagged layer (live network + Docker); nightly in CI |
| `make test-dash` | Run dash0 Playwright tests |
| `make lint` | Lint backend + dash |
| `make fmt` | Format all code |
| `make migrate` | Run database migrations |
| `make bench-checks` | Benchmark check throughput (SQLite + Postgres) |
| `make bench-memory` | Measure memory precisely — fresh server per repetition, fixed sampling protocol, inter-run spread, `--compare` against a saved baseline. `BENCH_MEM_MODE=docker` (with `make bench-memory-image`) is the authoritative run; the default local mode is labelled non-authoritative in its own report. See `wiki/runbooks/memory-profiling.md` §5 |

## Default credentials

| Mode | Email | Password | Org |
|---|---|---|---|
| Normal | `admin@solidping.io` | `solidpass` | `default` |
| Test (`SP_RUNMODE=test`) | `test@test.com` | `test` | `test` |

Rules:
- The seeded `admin@solidping.io` has `users.must_change_password`. This applies to every fresh database, including `make dev`.
- Its first login yields a session that reaches only `POST /auth/change-password`, `GET /auth/me` and `POST /auth/logout`. Everything else answers `403 PASSWORD_CHANGE_REQUIRED`, and dash0 lands on `/d/change-password`.
- Pick a new password, then use it in place of `solidpass` in every example.
- The test-mode user `test@test.com` is not flagged (created in `server/test/testdata/testdata.go`). Playwright suites sign in with these fixed credentials.

## SaaS mode & entitlements

`SP_DEPLOYMENT_MODE=saas` switches per-org defaults to the SaaS tier; the billing service (`../solidping-billing`) writes entitlements through a **signed** `PUT /api/v1/orgs/:org/entitlements`. Never collapse `entitlements.billing_upgrade_token_secret` into `billing_inbound_secret` (security regression). Full wiring, key rotation and operator migration: [wiki/features/saas-mode.md](wiki/features/saas-mode.md) and [wiki/features/entitlements.md](wiki/features/entitlements.md).

## Frontend UI conventions

Design reference (live): `http://localhost:4000/d/orgs/default/design-reference`. Source: [`web/dash0/src/routes/orgs/$org/design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx).

- Read it before any frontend change (new page, tweak, one-off component).
- It renders every shipped primitive with its import line. Reuse those, not raw Radix or custom code.
- If a needed primitive or pattern is missing, add it to the reference page in the same change.

Frontend rules:
- Every page must be usable on mobile: responsive layouts, no fixed widths, large touch targets.
- 401: redirect to login with `?returnTo={currentPath}`. 403: show "Permission Denied", never redirect (causes loops). See `wiki/conventions/frontend-errors.md`.
- Editing always navigates to a dedicated route (`/<resource>/new`, `/<resource>/$id`) — never in a modal dialog.
- Row actions: prefer two ghost icon buttons (`Pencil` / `Trash2`) over a `MoreVertical` menu.
- Every page runs under a Content-Security-Policy (spec 2026-09-25-28, `server/internal/securityheaders`, docs `web/docs/docs/configuration/security-headers.md`).
  - No `eval` / `new Function` anywhere (dash0 sets zod `jitless` for this).
  - No new inline `<script>` outside the SPA shell (shell scripts are hashed from the embedded build).
  - status0 may only fetch first-party (`img-src` / `font-src` / `connect-src 'self'`).
  - Add a new external origin dash0 needs to `dashboardPolicy()` with a comment saying why. Otherwise prod blocks it silently (`make dev` proxies to Vite, no CSP).
  - E2E `status-page-appearance.spec.ts` asserts zero violations.

## REST API conventions
- Wrap list responses in `{ "data": [...] }`, never return a bare array.
- Use `$uid` in URL paths (not `$id`).
- Use `PATCH` for updates, `q` for search, `limit` for page-size.
- camelCase for all JSON properties and query parameters.
- Multi-value query params use the singular form, comma-separated (e.g. `?checkUid=a,b`).
- Full endpoint list: `wiki/api-specification/`.

### Error shape
```json
{ "title": "Human message", "code": "MACHINE_CODE", "detail": "More detail" }
```
Key codes: `INTERNAL_ERROR`, `VALIDATION_ERROR`, `NOT_FOUND`, `UNAUTHORIZED`, `FORBIDDEN`, `CONFLICT`. See `server/internal/handlers/base/` for the full list.

### Quick API test
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.io","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks'
```

## Observability toggles

`SP_PROMETHEUS_ENABLED` (default true), `SP_METRICS_SCRAPE_TOKEN` (unset means `/metrics` 404s), `SP_PROFILER_ENABLED` (default false), `SP_OTEL_ENABLED` (default false) are independent. Details: [wiki/runbooks/observability-toggles.md](wiki/runbooks/observability-toggles.md).

## Testing
- **Backend**: table-driven tests + testcontainers for integration (see `server/AGENTS.md`)
- **Dash0**: Playwright E2E in `web/dash0/e2e/` (see `web/dash0/AGENTS.md`)
- Comprehensive coverage expected for new features

## Never name a real company — use `acme`

- Never write a real company name in this repository: specs, tests, fixtures, sample data, code comments, doc examples, commit messages, changelog, PR descriptions, wiki, issue reports.
- This covers every third party: employers, customers, vendors, organizations of bug reporters.
- Replace it with `acme`, keeping the shape of what you replace:

| Instead of | Use |
|---|---|
| a company name | `acme` |
| an org slug / handle | `acmetech`, `@acmetech/aws-paris` |
| a domain | `acme.com`, `status.acme.com` |
| an email | `alice@acme.com` |
| a person at a company | `alice` / `bob` (no surname, no employer) |

Why: the repository is public and a released tag cannot be recalled. Enforce the rule when writing, not in a later cleanup.

Exceptions:
- Names of technologies and services SolidPing integrates with (Slack, Telegram, OVH, Prometheus, Cloudflare...) are fine.
- Do not rewrite already-published history. If you find a real name in the tree, replace it and say so.

## Specs
- Filename format: `YYYY-MM-DD-NN-title.md` (`NN` unique per day across `specs/todos/` and `specs/done/YYYY/MM/`)
- Active: `specs/todos/`, Done: `specs/done/YYYY/MM/`, Backlog: `specs/backlog/`, Cancelled: `specs/cancelled/`

## Batch branches
`/implement-todos` and similar multi-spec runs integrate onto a dated batch branch (e.g. `batch/2026-06-23`).
- On a batch branch, never change the current branch. Keep it checked out on the batch branch and do all integration there.
- The working tree is shared with subagents and concurrent automations. A `git checkout` can strand the batch branch or race their git operations.
- If a step genuinely needs an isolated branch, use a separate `git worktree` instead of switching this tree's current branch.

## Agent instruction files

- Keep every `AGENTS.md` under 300 lines (hard limit 500) and 20 KB. Check: `make lint-agent-docs`.
- When a file grows, move reference material (schemas, cookbooks, long rationale) to `wiki/` and leave a one-line "read when" link. Keep only commands and rules here.
- Never run `git stash`, `git reset --hard` or `git clean` in a shared working tree (enforced for Claude by `.claude/settings.json`, applies to every agent).
- Migration rules: [wiki/conventions/migrations.md](wiki/conventions/migrations.md). Spec workflow: [wiki/conventions/specs-workflow.md](wiki/conventions/specs-workflow.md). Slash-command procedures: [wiki/conventions/agent-commands.md](wiki/conventions/agent-commands.md).
