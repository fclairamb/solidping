---
model: sonnet
effort: high
---

# Agent instructions are split over 6 CLAUDE.md files, Claude-only, oversized, and unenforced

## Problem

Agent guidance lives in `CLAUDE.md` files, a name only Claude Code reads. Other agents (Codex, Cursor, Copilot, Gemini CLI, Jules) read `AGENTS.md`. Sizes today (`wc -lc`):

| File | Lines | Bytes |
|---|---|---|
| `CLAUDE.md` | 207 | 15 KB |
| `server/CLAUDE.md` | 408 | 23 KB (over 20 KB) |
| `web/dash0/CLAUDE.md` | 339 | 15 KB |
| `wiki/CLAUDE.md` | 60 | 4 KB |
| `server/internal/checkers/CLAUDE.md` | 58 | 3 KB |
| `web/CLAUDE.md` | 12 | 0.7 KB |

The root file is loaded into every session. Long files bury the rules that matter, and recent models (Opus 5.5 / Sonnet 5.5) follow short, imperative, verifiable rules better than long prose and reference dumps. `server/CLAUDE.md` embeds material that is reference, not instruction (DB schema tables at ~l.120-210, curl cookbook at ~l.308-405, error code list at ~l.236).

Nothing enforces a size limit. `wiki/CLAUDE.md:15` has a 500/800 line rule for wiki markdown, also unenforced (no check in `Makefile` or `.github/workflows/`).

The `.claude/` directory holds generic content under a Claude-specific path: `.claude/skills/database.md`, `.claude/skills/tasks.md`, `.claude/commands/idea.md`, `.claude/commands/sync-pg-to-sqlite.md`, plus genuinely Claude-specific `settings.json` (git stash deny), `settings.local.json`, `launch.json`.

## Proposal

Rules for every agent instruction file (`AGENTS.md` at any level):
- Target under 300 lines, hard limit 500 lines.
- Under 20 KB (20480 bytes).

1. **Rename and rewrite.** `git mv` each `CLAUDE.md` to `AGENTS.md` (root, `server/`, `web/`, `web/dash0/`, `wiki/`, `server/internal/checkers/`). Add a one-line `CLAUDE.md` at each level containing only `@AGENTS.md` (Claude Code's import syntax), so Claude still loads it. Alternative if a symlink is preferred: check that Windows contributors and the Docker build context cope with it. Recommend the `@AGENTS.md` stub.
2. **Rewrite for Opus 5.5 / Sonnet 5.5.** In each file:
   - Lead with the commands (build, test, lint, dev) and the non-negotiable rules.
   - Imperative, one rule per line, each with its reason in a short clause and a verifiable check where one exists (a command or a grep).
   - Drop what the model can derive from the code (directory trees that `ls` shows, library lists that `go.mod` / `package.json` show).
   - Drop emphasis inflation (ALL CAPS, "IMPORTANT") except for the few real hard rules.
   - Keep long-lived hard rules currently in root `CLAUDE.md`: never name a real company (use `acme`), REST conventions, CSP rules, UI rules, batch-branch rule, specs naming.
3. **Move reference material out, link to it.** Make the split into `wiki/`, one focused file each, linked from the relevant `AGENTS.md` with a one-line "read when" hint:
   - `server/CLAUDE.md` DB schema section -> `wiki/database-model/` (check for overlap first, merge rather than duplicate).
   - curl/API testing cookbook and troubleshooting -> `wiki/runbooks/api-testing-with-curl.md`.
   - Error codes list -> `wiki/api-specification/` (or link to `server/internal/handlers/base/`).
   - SaaS mode & entitlements block (root, ~30 lines) -> `wiki/features/entitlements.md` (already exists per the file), leaving 3 lines in root.
   - Observability toggles table -> `wiki/runbooks/` or `web/docs` config page; keep a link.
   - dash0 "e2e type-checking and lint" and translations sections -> `web/dash0/` sub-docs or `wiki/testing/`.
4. **Generic agent config dir.** Move `.claude/skills/database.md`, `.claude/skills/tasks.md` to `wiki/conventions/` (plain markdown, agent-neutral) and reference them from `AGENTS.md`. Keep `.claude/skills/` as thin wrappers (frontmatter + "read `wiki/conventions/x.md`") only if Claude needs the skill trigger; otherwise delete. Move the two slash commands' bodies to `wiki/conventions/` or `specs/README` and keep `.claude/commands/*.md` as one-line pointers. Keep `.claude/settings.json`, `settings.local.json`, `launch.json` (Claude-specific). Note in `AGENTS.md` that the `git stash` ban is enforced for Claude via `settings.json` and applies to all agents. Do not invent a `.agents/` standard: no consensus exists, and `AGENTS.md` plus `wiki/` is the neutral layer.
5. **Enforce.** Add `scripts/check-agent-docs.sh` (bash, `set -euo pipefail`, `usage()` per repo bash conventions) that finds every `AGENTS.md` (excluding `node_modules`, `.claude/worktrees`, `.bb`, `.git`) and fails when:
   - lines > 500 (error), lines > 300 (warning, non-fatal),
   - bytes > 20480 (error),
   - a `CLAUDE.md` exists beside it with content other than the `@AGENTS.md` stub,
   - an `AGENTS.md` is missing next to a `CLAUDE.md`.
   Wire it as `make lint-agent-docs`, add to `make lint`, and run it in the lint job of `.github/workflows/ci.yml`. Use `wc -c` (not locale-dependent `du`).
6. **Fix references.** Update links to `CLAUDE.md` in `CONTRIBUTING.md` (lines 4, 12, 29, 45, 75, anchors included) and any spec/wiki links that point at moved sections (do not edit `specs/done/**`, historical). Update `wiki/CLAUDE.md`'s file-size rule to point at the new script and align its numbers (500 soft / 800 hard for wiki docs may stay, but state that `AGENTS.md` files use 300 / 500).
7. **Record the rule.** State the 300 / 500 / 20 KB rule in root `AGENTS.md` (one line) and how to move content out when a file grows.

Rewriting must not silently drop a rule: produce a checklist in the PR description mapping every removed section to where it went (moved, merged, or deleted as derivable).

## Tests
- `scripts/check-agent-docs.sh` self-test (a bash test under `scripts/` or a Make target using a temp dir): a fixture `AGENTS.md` of 250 lines passes; 350 lines passes with a warning; 501 lines fails; 300 lines but 21 KB fails; a non-stub `CLAUDE.md` beside an `AGENTS.md` fails; a stub `CLAUDE.md` (`@AGENTS.md`) passes; a `CLAUDE.md` with no `AGENTS.md` fails.
- Run `make lint-agent-docs` on the repo after the rewrite: must exit 0, every file under 500 lines and 20 KB, most under 300.
- Link check: grep the repo (excluding `specs/done`, `node_modules`, worktrees, `.bb`) for `CLAUDE.md#` anchors and for relative links to moved files; none may dangle.
- Manual: a fresh Claude Code session in `server/` still lists the rules (via the `@AGENTS.md` stub); confirm with `/memory`.

## To verify
- Whether `wiki/database-model/` already covers the `server/CLAUDE.md` schema section (merge, don't duplicate).
- Whether `.github/workflows/claude.yml` or other workflows read `CLAUDE.md` (the grep only found docs/specs, but confirm).
- Whether `.claude/skills/*.md` (flat files, not `name/SKILL.md`) are actually picked up by Claude Code; if not, they are dead weight and can simply move.
- `.claude/worktrees/` copies of the old files are gitignored stale worktrees; leave them alone.

## Open questions
- Keep a `CLAUDE.md` stub next to each `AGENTS.md`, or a symlink? Recommended: `@AGENTS.md` stub file (portable, no symlink issues on Windows).
- Apply the 300 / 500 / 20 KB limit to `wiki/` markdown too? Recommended: no, keep the wiki's own 500 / 800 rule and enforce it in the same script as a second check only if cheap.

## Resolved open questions
- Keep a `CLAUDE.md` stub file containing `@AGENTS.md` next to each `AGENTS.md`. No symlinks.
- Do not apply the 300 / 500 / 20 KB limit to `wiki/`. Keep the wiki's own 500 / 800 rule, and add it to the same script as a second check only if cheap.
