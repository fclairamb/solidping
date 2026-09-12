---
model: sonnet
effort: medium
---

# The repo is missing five of GitHub's community-standards files

## Problem

GitHub's **Community Standards** checklist (`/community` on the repo) is
green only on *Description*, *README* and *License*. Five items are still
marked as missing:

| Item | Status | Where GitHub looks |
|---|---|---|
| Code of conduct | missing | `CODE_OF_CONDUCT.md` (root, `docs/` or `.github/`) |
| Contributing | missing | `CONTRIBUTING.md` (root, `docs/` or `.github/`) |
| Security policy | missing | `SECURITY.md` (root, `docs/` or `.github/`) |
| Issue templates | missing | `.github/ISSUE_TEMPLATE/*.yml` + `config.yml` |
| Pull request template | missing | `.github/pull_request_template.md` |

Today `.github/` holds only `renovate.json` and `workflows/`, and the root has
`LICENSE` (AGPL-3.0) and `README.md`. The repository is public and accepts
outside bug reports, so the absence is felt in practice:

- **No security contact.** There is no address or process for reporting a
  vulnerability privately; a reporter's only option is a public issue.
- **Issues arrive unstructured.** Nothing asks for the SolidPing version,
  deployment mode (self-hosted / SaaS / agent), database type, or log
  excerpts — the things every triage starts by asking for.
- **PR titles fail CI silently for newcomers.** `pr-title.yml` rejects any
  non-conventional title (allowed types: `feat|fix|perf|revert|docs|style|chore|refactor|test|build|ci`)
  because release-please builds the changelog from the squash subject, but
  nothing tells a contributor that before they open the PR.
- **Local workflow is undocumented outside `CLAUDE.md`.** `make dev`,
  `make lint`, `make test`, `make test-dash`, the spec queue in `specs/`, and
  the "never name a real company, use `acme`" rule all live in AI-facing
  files that a human contributor is unlikely to read.

## Proposal

Add the five files, choosing the widely-adopted standard for each rather than
inventing house variants. Keep them short, point at existing docs instead of
duplicating them, and make the checklist fully green.

### 1. `CODE_OF_CONDUCT.md` — Contributor Covenant 2.1

Use the verbatim Contributor Covenant v2.1 text (the de-facto standard;
GitHub recognises it and links it from the checklist). The single blank to
fill is the enforcement contact — see *Open questions*.

### 2. `CONTRIBUTING.md` — a one-page on-ramp

Written for a human, not an agent. Sections, each a few lines linking out
rather than restating:

- **Before you start** — search issues, open one for anything beyond a small
  fix; larger work is tracked as a spec in `specs/todos/` (link the filename
  convention in `CLAUDE.md`).
- **Local setup** — prerequisites (Go 1.26+, Bun/Node for `web/`, Docker),
  then `docker-compose up -d`, `make dev`, `make dev-test`, and the default
  credentials table incl. the forced password rotation on a fresh DB.
- **Before opening a PR** — `make fmt`, `make lint`, `make test`,
  `make test-dash`; add a `CHANGELOG.md`-worthy description only if the
  change is user-visible (release-please writes the changelog from the PR
  title, per `wiki/conventions/changelog.md`).
- **PR titles are conventional commits** — quote the allowed-types list from
  `pr-title.yml` and explain *why* (squash subject → changelog); one PR per
  topic; the team squash-merges.
- **Never name a real company — use `acme`** — restate the `CLAUDE.md` rule
  in two sentences, since fixtures and bug reports are where outsiders
  contribute most.
- **Docs & wiki** — `web/docs/` is the published site, `wiki/` is internal
  engineering notes; competitor comparisons never go in `web/docs/`.
- **License** — contributions are AGPL-3.0 like the rest of the project.

### 3. `SECURITY.md` — private reporting first

- **Supported versions**: latest minor release only (the project is
  pre-1.0 and ships from `main` via release-please); state it as a table.
- **Reporting**: point to GitHub's *Report a vulnerability* private advisory
  form as the primary channel (`https://github.com/fclairamb/solidping/security/advisories/new`),
  with an email fallback. Ask reporters *not* to open a public issue.
- **What to expect**: acknowledgement within a stated window (propose 72 h),
  fix-and-disclose coordinated with the reporter, credit in the release
  notes if wanted.
- **Scope notes** that save round-trips: SaaS (`solidping.io`) and
  self-hosted share the codebase; agents (`sp agent`) and the browser-check
  CDP sidecar are in scope; third-party integrations' own services are not.

Enabling *private vulnerability reporting* is a repo setting, not a file —
the spec's acceptance includes flipping it on so the link in `SECURITY.md`
actually works.

### 4. `.github/ISSUE_TEMPLATE/` — three YAML forms + `config.yml`

Use issue **forms** (`.yml`), not markdown templates, so fields are
structured and required where it matters:

- `bug_report.yml` — labels `bug`; fields: what happened / expected,
  reproduction steps, **SolidPing version** (from `/api/mgmt/version` or the
  sidebar), **deployment** dropdown (self-hosted Docker, self-hosted binary,
  SaaS, agent-only), **database** dropdown (PostgreSQL, SQLite), check type
  involved (optional), relevant logs (`logs/backend.log` for dev, container
  logs otherwise — with a reminder to redact tokens and real hostnames).
- `feature_request.yml` — labels `enhancement`; problem, proposed behaviour,
  alternatives, willingness to contribute.
- `check_type_or_integration.yml` — labels `enhancement, integration`; the
  most common ask on a monitoring tool: which protocol/service, a link to its
  API docs, whether it is for checks (probing) or notifications (delivery).
- `config.yml` — `blank_issues_enabled: false`; contact links to the docs
  site (`https://solidping.io/docs`), the security advisory form, and the
  `migrate-from-*` guides for "how do I import from X" questions.

Keep every form under ~40 lines; nobody fills in long forms.

### 5. `.github/pull_request_template.md`

A compact checklist, not an essay:

```markdown
<!-- Title must be a conventional commit: feat|fix|perf|revert|docs|style|chore|refactor|test|build|ci
     e.g. "fix(checks): honour HTTP cookie jar across redirects" — it becomes the changelog line. -->

## What & why

## How to test

## Checklist
- [ ] `make fmt && make lint && make test` pass locally (`make test-dash` if `web/` changed)
- [ ] Tests added or updated for the change
- [ ] Docs updated (`web/docs/`) if user-visible; wiki (`wiki/`) if operational
- [ ] No real company / customer / person named — `acme` only
- [ ] Migrations added under `server/migrations/` if the schema changed
```

### Placement & wiring

- Root for `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md`, `SECURITY.md` (most
  discoverable; GitHub also links them from the *New issue* / *New PR* UI).
- `.github/` for the templates.
- Add a short **Contributing** section to `README.md` linking to
  `CONTRIBUTING.md` and `SECURITY.md` — the README currently has no such
  pointer.
- `CHANGELOG.md`: this is `docs:` scope; no user-facing entry needed beyond
  what release-please generates from the PR title.
- Lint: the repo's markdown is not linted in CI, so no toolchain change.

### Acceptance

- `https://github.com/fclairamb/solidping/community` shows all seven items
  green after merge.
- Opening a new issue on GitHub presents the three forms and no blank
  option; each form renders without YAML errors (GitHub validates on push —
  check the *Issues → New issue* page after the PR merges, or use
  `gh api repos/fclairamb/solidping/issues/templates`-style inspection if
  available).
- Opening a PR pre-fills the template.
- Private vulnerability reporting is enabled in repo settings and the
  advisory link in `SECURITY.md` resolves.

## Open questions

1. **Security / conduct contact address.** No `security@solidping.io` or
   similar exists anywhere in the repo today (only `admin@` / `demo@`
   seeded users, and the `acme` placeholder in tests). Either create
   `security@solidping.io` on the mail host and use it in both
   `SECURITY.md` and the Code of Conduct enforcement line, or use the
   GitHub advisory form alone plus the maintainer's GitHub handle. Decide
   before merging; a dead address is worse than none.
2. **Response-time commitment** in `SECURITY.md` — 72 h acknowledgement is
   proposed; adjust to what a single-maintainer project can honour.

## Resolved open questions

Answered by the repository owner on 2026-09-12. These are decisions, not
suggestions — implement them as written.

> **1. Security / conduct contact address.** No `security@solidping.io` or
> similar exists anywhere in the repo today… Either create
> `security@solidping.io` on the mail host and use it in both `SECURITY.md` and
> the Code of Conduct enforcement line, or use the GitHub advisory form alone
> plus the maintainer's GitHub handle. Decide before merging; a dead address is
> worse than none.

**Decision: use `security@solidping.io`.** The owner is creating the address on
the mail host and redirecting it to their personal inbox, so it is a live
mailbox, not a placeholder. Use `security@solidping.io` in **both**
`SECURITY.md` and the Code of Conduct enforcement line.

Two constraints on the implementer:

- **Do not create, configure, or verify the mailbox.** It is an out-of-repo
  action the owner is handling. Write the address into the files and stop.
- **Do not put the forwarding target in the repo.** The address forwards to a
  personal inbox; that destination is not to appear in `SECURITY.md`, the Code
  of Conduct, a comment, or a commit message. `security@solidping.io` is the
  only address that gets written down.
- Keep GitHub private vulnerability reporting as the *other* channel, as the
  spec already proposes — the email address is additive, not a replacement, so
  a reporter who prefers the advisory form still has it.

> **2. Response-time commitment** in `SECURITY.md` — 72 h acknowledgement is
> proposed; adjust to what a single-maintainer project can honour.

**Decision: best effort, no fixed window.** Drop the 72-hour commitment. Say
that reports are acknowledged as quickly as the maintainer can manage, and do
**not** state a number, an SLA, or a business-day window anywhere in
`SECURITY.md`. Rationale: this is a single-maintainer project, and a stated
window that is missed reads worse than never having promised one.
