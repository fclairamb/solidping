---
model: sonnet
effort: high
---

# The repo has never been proven to work in a Claude cloud (remote) session

## Problem

A Claude cloud session starts from a fresh clone in a Linux sandbox: no `.env`, no `*.priv*` secrets (gitignored, `.gitignore:9,20`), no Docker daemon, no local Postgres, no `node_modules`, no prebuilt frontend, no MCP servers, no `~/.claude` user config, no `rtk`, no VPN. Everything the repo documents as "the way to work" assumes the maintainer's laptop. Suspected blockers, to confirm by trying (see To verify):

- **Postgres via Docker.** `CLAUDE.md` step 1 is `docker-compose up -d` (`docker-compose.yml`, postgres:18.6 on 55432). Cloud has no Docker. SQLite and `SP_DB_TYPE=postgres-embedded` exist (`Makefile:201,226`) but the docs never say "use SQLite when there is no Docker", and `make test` is SQLite-only (`-short`, `Makefile:357`) so that path is probably fine, but `make dev` has not been checked against SQLite.
- **No bootstrap.** `make deps` (`Makefile:456`) installs Go modules and `bun install` for dash0 and status0, but there is no single setup script, and nothing installs the toolchain itself: Go 1.26.3 (`server/go.mod:3`), bun, golangci-lint (CI pins a version, `.github/workflows/ci.yml:180`), Playwright browsers. Cloud sessions run a setup script once per environment; none exists in the repo.
- **Embedded frontend not built.** The Go binary embeds `dash0res` (`Makefile:110-115`), built from `web/dash0/dist` which is gitignored (`web/dash0/.gitignore:5`). A fresh clone may fail `go build` or serve a stale/empty UI until `build-dash0` + `copy-dash0` run (see memory of the same trap with `make build-backend`). Check whether `go build ./...` and `go test ./...` compile on a clean clone.
- **Undocumented required env.** `.env.example` is all optional integrations. The CI E2E job sets env vars (rate limiting off, registration pattern) that a bare `make dev-test` lacks; the local-vs-CI difference produces false test failures. Not written down in one place.
- **Sandbox network allowlist.** Cloud sessions restrict outbound hosts. `go mod download` (proxy.golang.org, sum.golang.org), `bun install` (registry.npmjs.org), Playwright browser download (cdn.playwright.dev / playwright.download.prss.microsoft.com), embedded-postgres binary download (Maven Central) all need to be reachable. Checkers' own tests reach live hosts only under `slowtests` (fine).
- **Instructions written for the laptop.** `CLAUDE.md` mentions localhost:4000 hot reload, `rtk` prefixes (user-global, not in repo), the k8xp VPN, `gopass`; `.claude/settings.json` denies `git stash` (fine). `make dev` uses `devloop` supervisor with logs in `logs/*.log`, which is fine but the agent must know to background it.
- **Stray secrets in the working tree.** Untracked but present locally: `*.priv.json`, `*.priv.har`, `agent-keys.json`, `.env`. Not in a clone, but confirm none are tracked (`git ls-files | grep -i priv`) since a cloud session would upload the clone.
- **Long-running/slow targets.** `make test-postgres` ~12 min, E2E needs a browser and a running server; cloud sessions have time and memory limits. Document which checks a cloud agent should run (`make lint`, `make test`, scoped `bun run test:unit`, scoped Playwright file) and which it should leave to CI.

## Proposal

1. **Probe first.** In a Claude cloud session (or a clean Linux container with no Docker, no `.env`, empty caches, network limited to the hosts above), clone the repo at `main`, follow only `CLAUDE.md`, and record every failure in `wiki/runbooks/claude-cloud.md` (new). This list drives the rest; do not fix things not observed failing.
2. **Bootstrap script.** Add `scripts/cloud-setup.sh` (bash, `set -euo pipefail`, `usage()` per the repo bash conventions) idempotent, no Docker, no secrets: verify or install Go (version from `server/go.mod`), bun, golangci-lint (version from CI), then `make deps`, then build dash0 + status0 and copy embedded resources so `go build ./...` works. Optional `--with-playwright` installs Chromium only. Prints what it skipped. Ship it as the documented cloud "setup script" contents.
3. **Make no-Docker the documented default for agents.** In `CLAUDE.md` (or `AGENTS.md` if spec 2026-09-29-07 has landed), add a short "Without Docker (cloud sessions, CI-like sandboxes)" block: `SP_DB_TYPE=sqlite` (or `postgres-embedded`), the exact command to run the server for a smoke test, default credentials, and which `make` targets to run. Verify `make dev` / `make dev-test` work with SQLite; if they hard-require Postgres, fix the Makefile/devloop default or add `make dev-sqlite`.
4. **Fix compile-from-clean.** If a clean clone fails `go build ./...` or `go vet` because embedded frontend directories are empty, commit a placeholder (e.g. `.gitkeep`/stub `index.html` guarded by ignore rules) or make the embed tolerate absence, so backend-only work needs no frontend build. Files: `server/internal/**/dash0res`, `status0res` embed sites (locate with `grep -rn 'go:embed' server --include='*.go'`), `Makefile:110`.
5. **Env parity note.** Document in the runbook the env vars CI sets for E2E that `make dev-test` needs to match (copy from `.github/workflows/ci.yml`, do not invent), and add them to a `make dev-test` default or a checked-in `.env.ci.example` if that is small.
6. **Trim laptop-only assumptions.** Mark laptop-only guidance (rtk, VPN, k8xp, gopass, `:4000 already running`) as such, or move it to the user's global config, so it does not mislead a cloud agent. Keep it short; do not restructure the file (that is spec 07).
7. **Cloud agent guardrails.** Add "what to run before pushing from a sandbox" (lint + `make test` + scoped unit tests, leave Postgres/E2E layers to CI) to the runbook and link it from the top-level instructions.
8. **Acceptance run (the test of the work).** After steps 1-7, in a fresh cloud session with only the setup script, have Claude implement the smoke feature below end to end, on a `feat/` branch with a PR, using only the documented commands. Any friction found there is fixed in the docs/script and the run repeated until it is clean.

**Smoke feature (no SQL migration):** add a `GET /api/v1/orgs/:org/checks/count` style endpoint is too close to auth; pick instead a pure, side-effect-free one: expose the server's build `commit` and `startedAt` uptime seconds in `GET /api/mgmt/version` if not already present (check the existing handler and OpenAPI spec `server/internal/app/openapi/openapi.yaml`). It touches handler, OpenAPI, one table-driven Go test, and needs no DB change. If `/api/mgmt/version` already returns both, substitute another read-only field and record that in the PR.

## Tests
- `scripts/cloud-setup.sh`: run twice in a clean container without Docker; second run makes no changes and exits 0 (idempotent). Run with Go missing and network blocked: exits non-zero with a message naming the unreachable host (negative case).
- Clean-clone check (CI job or `make check-clean-clone`, nightly is enough): fresh `git clone`, `go build ./...` and `make test` succeed with no frontend build and no Docker. This is the regression guard for step 4.
- The smoke feature's own tests: Go handler test in `server/internal/handlers/system/` (or wherever `/api/mgmt/version` lives) asserting the new field is present and correctly typed, and a negative case (field absent when not built with version info falls back to a documented default, not a 500).
- Acceptance: the smoke-feature PR from step 8 passes `make lint` and `make test`, and the PR description lists the commands the cloud agent used; zero commands outside the runbook.

## To verify
- Whether `go build ./...` / `go test ./...` compile on a clean clone (embed directories present or not).
- Whether `make dev` runs on SQLite with no Docker, and what `SP_DB_TYPE` default is (`server/internal/config/config.go`).
- The exact outbound hosts a Claude cloud environment allows and whether Playwright/embedded-postgres downloads are reachable; adjust the setup script or document a custom network allowlist accordingly.
- Which of `.claude/skills/*`, `.claude/commands/*`, `.claude/settings.json` are honoured in cloud sessions (repo-level `.claude/` should be), and that `.claude/worktrees/` (gitignored) is absent.
- `git ls-files | grep -Ei 'priv|\.har$|agent-keys'` is empty (no secrets tracked).
- Whether `/api/mgmt/version` already carries commit and uptime (picks the smoke feature).

## Open questions
- Should the setup script install toolchains itself, or only verify and print instructions (cloud images often preinstall Go and Node but not bun)? Recommended: verify, and install only bun and golangci-lint by pinned download into `~/.local/bin`; fail with instructions for Go.
- Is the smoke feature choice acceptable, or do you want a specific one? Recommended: the `/api/mgmt/version` addition, it is read-only, migration-free and has an existing test home.

## Resolved open questions
- The setup script verifies toolchains. It installs only bun and golangci-lint, by pinned download into `~/.local/bin`, and fails with instructions for Go.
- The smoke feature is the read-only `/api/mgmt/version` addition.
