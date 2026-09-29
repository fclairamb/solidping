# Agent slash-command procedures

Agent-neutral bodies of the repo's slash commands. `.claude/commands/*.md` are one-line pointers here.

## idea

**Usage**: `/idea <description>`

## Steps

1. Derive a slug from the description (lowercase, hyphenated)
2. Create `specs/ideas/YYYY-MM-DD-<slug>.md` using the template in [specs-workflow.md](specs-workflow.md)
3. Flesh out the Problem and Proposal sections based on the description
4. Commit: `docs: add idea spec for <title>`

## sync-pg-to-sqlite

Ensure SQLite matches PostgreSQL implementation.

**Verify**:
1. Migration files have SQLite equivalents with matching `NNN` prefix (both engines share the same sequence number per release)
2. SQL queries work on both databases
3. Data types are compatible
4. Tests run on both backends

**At release time (consolidation)**: both engines are squashed together into `NNN_vX_Y_Z.up.sql` / `.down.sql`, which re-syncs their numbering. The scratch migrations in both dirs are deleted at the same time.
