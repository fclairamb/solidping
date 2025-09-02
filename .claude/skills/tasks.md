# Specs / Task Management

SolidPing uses a specs-based workflow for planning and implementing features.

## Directory Layout

```
specs/
  ideas/       # Rough ideas and brainstorming
  questions/   # Open questions needing discussion
  backlog/     # Validated specs ready to be scheduled
  todos/       # Specs ready for implementation (picked up by /implement-todos)
  done/YYYY/MM/ # Completed specs, archived by date
  cancelled/   # Abandoned specs
```

## Lifecycle

```
ideas/ → backlog/ → todos/ → (implement) → done/YYYY/MM/
                                            cancelled/
```

## Spec File Convention

- Filename: `YYYY-MM-DD-<slug>.md` (date prefix mandatory)
- Slugs: lowercase, hyphen-separated, descriptive

### Template

```markdown
# <Title>

## Problem
What problem does this solve?

## Proposal
How should we solve it?

## Acceptance Criteria
- [ ] ...
```

## Rules

- Use `git mv` for all moves between directories to preserve history
- Commit after each action
- When searching by slug, match partially (e.g., `credentials` matches `2025-12-06-credentials.md`)
- Specs in `todos/` are processed oldest-first by `/implement-todos`
- Archived specs go to `specs/done/YYYY/MM/` based on their date prefix
