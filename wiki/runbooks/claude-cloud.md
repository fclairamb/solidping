# Working in a Claude cloud (remote) session

A cloud session is a fresh clone in a Linux sandbox: no `.env`, no secrets, no Docker, no Postgres, no `node_modules`, no built frontend, no `rtk`, no VPN, no `~/.claude`. Everything below works in that shape.

## Setup (once per environment)

Use this as the environment's setup script:

```bash
scripts/cloud-setup.sh                    # add --with-playwright for E2E
```

It verifies Go (>= `server/go.mod`, never installs it, fails with a download link), installs bun and golangci-lint (version read from `.github/workflows/ci.yml`) into `~/.local/bin` by pinned download, runs `make deps`, then builds and embeds dash0 and status0. A second run changes nothing and prints what it skipped. Self-test: `scripts/cloud-setup_test.sh`.

Outbound hosts needed: `proxy.golang.org`, `sum.golang.org`, `registry.npmjs.org`, `github.com` (bun, golangci-lint), and for `--with-playwright` `cdn.playwright.dev`. The script names the host it could not reach. The exact allowlist of a Claude cloud environment is not verified from the repo: if a step fails on a blocked host, add it to the environment's custom allowlist.

## What was probed and found

Probed with a fresh `git clone` of HEAD, no frontend build, no Docker, no `.env`.

| Finding | Fix |
|---|---|
| `go build ./...` failed: `//go:embed all:dash0res` (and `status0res`, `docsres`) matched no files because the directories are gitignored. | A tracked `.gitkeep` in each directory (ignore rule `dir/*` + `!dir/.gitkeep`) so the embed always resolves. `copy-dash0`, `copy-status0`, `copy-docs` keep it. |
| `go test ./...` needs placeholder bundles (CI creates them in a workflow step). | `make embed-placeholders` (`scripts/embed-placeholders.sh`, creates only missing files, mirrors the CI step). `make test` runs it first. |
| The docs said `docker-compose up -d` first. | Not needed: `database.type` defaults to `sqlite`. See "Run the server" below. |
| No single bootstrap. | `scripts/cloud-setup.sh`. |
| Secrets tracked in git? | `git ls-files \| grep -Ei 'priv\|\.har$\|agent-keys'` only matches source files about private locations, no secret files. |

Guard: `make check-clean-clone` (`scripts/check-clean-clone.sh`) clones HEAD and runs `go build ./...` plus `make test`. Nightly is enough.

## Run the server (no Docker)

```bash
cd server && SP_RUNMODE=test SP_DB_TYPE=sqlite SP_DB_DIR=$(mktemp -d) go run . serve   # background it
curl -s localhost:4000/api/mgmt/version
```

Test mode logs in as `test@test.com` / `test` / org `test`. Normal mode seeds `admin@solidping.io` with a forced password change (see `AGENTS.md`). `make dev` and `make dev-test` also default to SQLite when `SP_DB_TYPE` is unset; they start bun dev servers, so run `scripts/cloud-setup.sh` first and background them (logs in `logs/*.log`).

## Env parity with CI (E2E)

The CI E2E job (`.github/workflows/ci.yml`, "E2E" job env) runs the server with these. `make dev-test` now sets the first three, so a local run matches:

| Variable | CI value | Why |
|---|---|---|
| `SP_RUNMODE` | `test` | seeds `test@test.com` |
| `SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE` | `0` | specs hammer the API |
| `SP_SERVER_RATE_LIMITING_MAX_CONCURRENT` | `0` | same |
| `SP_AUTH_REGISTRATION_EMAIL_PATTERN` | `.*` | register-and-confirm specs; read once at boot |
| `SP_DB_RESET` | `true` | fresh database per run |
| `PORT` | `4000` | listen port |
| `SP_DB_TYPE` / `SP_DB_URL` | `postgres` / `postgres://solidping:solidping@localhost:55432/solidping?sslmode=disable` | CI only; SQLite is fine locally |

A side-car server missing any of the first four produces false test failures.

## Before pushing from a sandbox

Run:

1. `make lint` (agent docs, golangci-lint, dash0 and status0 eslint). dash0 eslint has known pre-existing errors on base: only fix new ones.
2. `make test` (backend, `-short`, SQLite only).
3. For frontend changes: `bun run test:unit` in `web/dash0`, plus the one Playwright file you touched against a side-car server on a free port (`E2E_BASE_URL=http://localhost:<port>/d/`).

Leave to CI: `make test-postgres` (~12 min), `make test-slow` (live network + Docker), the full Playwright suite.

## Repo-level agent config

`AGENTS.md` is the single instruction file (`CLAUDE.md` files are `@AGENTS.md` stubs). Repo-level `.claude/` (settings, skills, commands) is read from the clone; `.claude/worktrees/` is gitignored and absent. Laptop-only guidance (`rtk`, VPN, gopass, an already-running `:4000`) lives in the user's global config and is marked as such in `AGENTS.md`.
