# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Core technologies
- **Backend**: Go 1.24+ (see `server/CLAUDE.md` for details)
- **Dashboard**: React + TanStack Router (see `web/dash0/CLAUDE.md` for details) — do not use `web/dash` for current development
- **Infrastructure**: Docker Compose with PostgreSQL for monitoring data storage
- **Monitoring**: Multi-protocol ping/health checking with distributed worker system
- **Docs site**: Docusaurus in `web/docs/` (baseUrl `/docs/`), embedded in the Go binary and served at the **`/docs`** path on every host (like `/dash0`, `/status0`) — so `solidping.io/docs` works with no extra infra. `docs.solidping.io` redirects its root into `/docs` (config `server.docs_host` / `SP_DOCS_HOST`). The API reference is generated at build from `server/internal/app/openapi/openapi.yaml`; the interactive OpenAPI (Swagger) explorer is at `/openapi`. `/docs/changelog` is generated at build from the root `CHANGELOG.md` (see `wiki/conventions/changelog.md` for entry-writing conventions). `docusaurus-plugin-llms` generates `llms.txt` / `llms-full.txt` from the docs content; they're served both at `/docs/llms.txt` / `/docs/llms-full.txt` and, for crawler convenience, at the conventional root path `/llms.txt` / `/llms-full.txt` (same embedded file, no duplication). The marketing site (`www.solidping.io`) is the separate `solidping-website` repo; internal engineering notes live in `wiki/`. **Competitor comparisons never go in the published docs site** — all competitor/comparison content belongs in `wiki/competitors/` (one `{name}.md` per competitor, indexed in `wiki/README.md`). The only competitor-facing pages `web/docs/` carries are the `migrate-from-*.md` import guides.

## Development workflow
If the server is running on port 4000, apply code changes directly — `make dev` / `make dev-test` hot-reloads both backend and frontend.

1. Start infrastructure: `docker-compose up -d`
2. Run everything: `make dev` (backend + dash0 + status0 with hot reload)
3. Test mode: `make dev-test` (same but with `SP_RUNMODE=test`)
4. Database changes: add migrations, then `make migrate`

Dev logs live in `logs/*.log` (`backend.log`, `dash0.log`, `status0.log`), size-rotated with `.1`/`.2` suffixes (~20 MB cap); all three processes run as children of the `server/cmd/devloop` supervisor, so Ctrl-C stops everything.

### Key Makefile targets
| Target | Purpose |
|---|---|
| `make build` | Build everything |
| `make dev` | Hot-reload backend + dash0 + status0 |
| `make dev-test` | Same, with `SP_RUNMODE=test` |
| `make dev-saas` | Same, in SaaS mode — pairs with `../solidping-billing` `make dev` (see SaaS mode below) |
| `make test` | Run backend tests |
| `make test-dash` | Run dash0 Playwright tests |
| `make lint` | Lint backend + dash |
| `make fmt` | Format all code |
| `make migrate` | Run database migrations |
| `make bench-checks` | Benchmark check throughput (SQLite + Postgres) |

## Default credentials

| Mode | Email | Password | Org |
|---|---|---|---|
| Normal | `admin@solidping.io` | `solidpass` | `default` |
| Test (`SP_RUNMODE=test`) | `test@test.com` | `test` | `test` |

**The normal seeded admin lands on a forced password rotation.** On a fresh
database the seeded `admin@solidping.io` carries `users.must_change_password`,
so the first successful login yields a session that can reach only
`POST /auth/change-password`, `GET /auth/me` and `POST /auth/logout` —
everything else answers `403 PASSWORD_CHANGE_REQUIRED` and dash0 lands on
`/dash0/change-password`. This is unconditional: `make dev` against a fresh
database prompts for a new password too. Pick one, and every example below
works with it in place of `solidpass`.

The test-mode user (`test@test.com`) is deliberately **not** flagged — it is
created by a different path (`server/test/testdata/testdata.go`) and the
Playwright suites sign in with those fixed credentials.

## SaaS mode & entitlements

`SP_DEPLOYMENT_MODE=saas` switches per-org defaults to the SaaS tier and lets a
separate billing service (`../solidping-billing`) drive plan upgrades. Per-org
limits live in `org_entitlements` (`maxChecks`, `maxUsers` — `maxSsoUsers` is a
deprecated decode-only alias, `maxChecksPerMinute`, `maxSlos`) plus display-only plan identity (`displayName`,
`displayEmoji`, e.g. "🚀 Team") — both shown on the org **Usage** page
(`/orgs/$org/organization/usage`).

The billing service writes entitlements via `PUT /api/v1/orgs/:org/entitlements`.
It proves identity by **signing** the request — HMAC-SHA256 over
`<timestamp>.<METHOD>.<path>.<sha256 body>`, sent as `X-SP-Signature: v1,<b64>` /
`X-SP-Timestamp` / `X-SP-Key-Id` — verified by the `ServiceSignature` middleware
(`internal/middleware/auth.go`, scheme in `internal/servicesig`) ahead of the
normal `RequireAuth` chain, so cross-org writes stay possible. Keys live in two
independent ordered `{id, secret}` sets, one per direction:
`entitlements.service_signing_keys` (verify the inbound push) and
`entitlements.outbound_signing_keys` (sign our calls to billing). Rotation is
"add the new key to both sides, then drop the old" — no lockstep restart.

The original static bearer (`entitlements.service_token`, let through by
`ServiceTokenBypass`) is **legacy**: still accepted while
`entitlements.allow_legacy_service_token` is true (the default) and logged as
deprecated on every use, so retiring it is a parameter flip rather than a
coordinated deploy. See `wiki/features/entitlements.md` for the migration order.

The `#bt=` upgrade token appended to the dashboard's `upgradeUrl` is signed with
its **own** secret, `entitlements.billing_upgrade_token_secret` (env
`SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET`, mirroring billing's
`BILLING_UPGRADE_TOKEN_SECRET`). `entitlements.billing_inbound_secret` is a
**bearer only** — leaking a credential that travels on every service call must
not also be the power to mint an upgrade token for any org, so collapsing the two
back into one value is a security regression. While the dedicated parameter is
unset the minter falls back to the bearer (WARN once per process); if both are
set to the *same* value, boot logs an ERROR and still starts. Operator migration
(both ends prefer-new / accept-old, so deploy order does not matter):

1. Deploy this — nothing moves, the fallback mints exactly as before.
2. Generate one new secret, set it on both sides.
3. Confirm billing's fallback warning has stopped.
4. Set `BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET=false` on billing.

**Step 4 is what closes the vulnerability** — steps 1–3 only make it closeable.

`make dev-saas` seeds the SaaS system parameters from
`SP_ENTITLEMENTS_SERVICE_TOKEN`, `SP_ENTITLEMENTS_SERVICE_SIGNING_KEYS`,
`SP_ENTITLEMENTS_OUTBOUND_SIGNING_KEYS`,
`SP_ENTITLEMENTS_ALLOW_LEGACY_SERVICE_TOKEN` and
`SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE` (the dashboard "Upgrade" link target);
run it alongside `../solidping-billing` `make dev` for the full upgrade loop.
See `server/internal/app/saas.go`.

## Frontend UI conventions

> **Before writing or modifying any UI**, check the live design reference at
> `http://localhost:4000/dash0/orgs/default/design-reference` — source:
> [`web/dash0/src/routes/orgs/$org/design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx).
>
> It renders every shipped primitive (buttons, alerts, dialogs, tables, forms, name+slug pairs…) with the exact import line alongside it. **Reuse those components and patterns** — don't reach for a raw Radix primitive or a custom implementation if the design reference already ships what you need.
>
> **This is mandatory for _any_ frontend change** — new pages, tweaks to existing UI, or one-off components alike. Always start from [`web/dash0/src/routes/orgs/$org/design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx); it is the single source of truth for components and conventions. If a needed primitive or pattern is missing, add it to the reference page as part of your change so the catalog stays canonical.

Additional frontend rules:
- **All pages must be fully usable on mobile** — use responsive layouts, avoid fixed widths, ensure touch targets are large enough.
- **401** → redirect to login with `?returnTo={currentPath}`; **403** → show "Permission Denied", never redirect (causes loops). See `wiki/conventions/frontend-errors.md`.
- Editing always navigates to a dedicated route (`/<resource>/new`, `/<resource>/$id`) — never in a modal dialog.
- Row actions: prefer two ghost icon buttons (`Pencil` / `Trash2`) over a `MoreVertical` menu.
- **Delete is always red, always a trash bin** — every delete/irreversible action uses the `Trash2` icon in the destructive red (`variant="destructive"`, or `text-destructive` for icon buttons and dropdown items). Never delete with a different icon or color, and never use destructive red for non-destructive actions.

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

Three independent env vars control which observability surfaces are active:

| Env var | Default | Effect |
|---|---|---|
| `SP_PROMETHEUS_ENABLED` | `true` | Gates the `/metrics` HTTP handler. When `false`, the endpoint returns 404. Metric collection itself stays on — only the scrape endpoint is gated. |
| `SP_PROFILER_ENABLED` | `false` | Starts the pprof HTTP server. Listen address controlled by `SP_PROFILER_LISTEN` (default `localhost:6060`). |
| `SP_OTEL_ENABLED` | `false` | Enables OpenTelemetry span export. HTTP and DB instrumentation record spans only when this is `true`. |

All three are independent — enabling one does not enable any other.

## Testing
- **Backend**: table-driven tests + testcontainers for integration (see `server/CLAUDE.md`)
- **Dash0**: Playwright E2E in `web/dash0/e2e/` (see `web/dash0/CLAUDE.md`)
- Comprehensive coverage expected for new features

## Never name a real company — use `acme`

**No real company name ever appears in this repository.** Not in specs, tests, fixtures,
sample data, code comments, doc examples, commit messages, changelog entries, PR
descriptions, wiki pages, or issue reports. This applies to every third party: employers,
customers, vendors, and the organizations of people who report bugs.

Replace the name with **`acme`**, keeping the shape of whatever you're replacing so the
example still reads naturally:

| Instead of | Use |
|---|---|
| a company name | `acme` |
| an org slug / handle | `acmetech`, `@acmetech/aws-paris` |
| a domain | `acme.com`, `status.acme.com` |
| an email | `alice@acme.com` |
| a person at a company | `alice` / `bob` (no surname, no employer) |

Why: this repository is public, and a bug report or a realistic-looking test fixture is a
poor place to disclose who a customer is, what they run, or that they had an outage. A
name that reaches a released tag cannot be recalled — scrubbing the working tree later
does not rewrite published history. So the rule is enforced at the point of writing, not
by a later cleanup pass.

Two things this rule does *not* mean: it does not apply to the names of the technologies,
libraries and services SolidPing genuinely integrates with (Slack, Telegram, OVH,
Prometheus, Cloudflare and so on — naming those is unavoidable and correct), and it is not
a reason to strip a real name from something that is already published. If you find a real
name in the repo, replace it and say so; do not quietly rewrite history around it.

## Specs
- Filename format: `YYYY-MM-DD-NN-title.md` (`NN` unique per day across `specs/todos/` and `specs/done/YYYY/MM/`)
- Active: `specs/todos/`, Done: `specs/done/YYYY/MM/`, Backlog: `specs/backlog/`, Cancelled: `specs/cancelled/`

## Batch branches
`/implement-todos` and similar multi-spec runs integrate onto a dated **batch branch** (e.g. `batch/2026-06-23`).
- **When the working tree is on a batch branch, never change the current branch.** Keep it checked out on the batch branch and do all integration there.
- The working tree is shared — subagents and concurrent automations run against it — so a `git checkout` onto another branch can strand the batch branch, race another automation's git operations, or leave the tree parked on a feature branch after a crashed step.
- If a step genuinely needs an isolated branch, use a separate `git worktree` instead of switching this tree's current branch.
