# Specs Directory Reorganization

## Goal
Reorganize the specs directory structure for clearer lifecycle semantics.

## Changes

### 1. Rename `specs/past/` → `specs/done/`
- Move all existing files from `specs/past/YYYY/MM/` to `specs/done/YYYY/MM/`
- Remove `specs/past/` after migration

### 2. Create `specs/cancelled/`
- New directory for specs that were abandoned or cancelled
- Same `YYYY/MM/` subdirectory structure as `specs/done/`

### 3. Rename `specs/next/` → `specs/backlog/`
- Move all existing files from `specs/next/` to `specs/backlog/`
- Remove `specs/next/` after migration

### 4. Update `/implement-todos` command
- In `.claude/commands/implement-todos.md`, change the archive step (E) to move completed specs to `specs/done/YYYY/MM/` instead of `specs/past/YYYY/MM/`

### 5. Update `CLAUDE.md`
- Update the Specs section to reference `specs/done/` and `specs/backlog/` instead of `specs/past/`
- Document the `specs/cancelled/` directory

## Lifecycle

```
specs/backlog/  →  specs/todos/  →  specs/done/YYYY/MM/
                                 →  specs/cancelled/YYYY/MM/
```
