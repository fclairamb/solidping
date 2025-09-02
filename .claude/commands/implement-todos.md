Implement all spec files in `specs/todos/` sequentially, with commits at each major step.

IMPORTANT: This is an **automated process**, and as such you should NOT ask for confirmation unless you're doing destructive actions.

## Process

1. **Discover**: List all `specs/todos/*.md` files sorted by date (oldest first). If there's none, cancel any active `/loop` (run `/cancel-ralph`) and stop here.
2. **For each spec**, run the loop below
3. **Report**: Once all todos are done, print a summary of what was implemented. Then re-check `specs/todos/` — if new specs appeared, go back to step 1; otherwise, cancel any active `/loop` (run `/cancel-ralph`) and stop.

## Per-spec loop

### A. Start

- Read the spec file fully
- Create a branch: `feat/<slug>` (or `fix/<slug>` / `chore/<slug>` based on spec content)
- Commit: `chore: start implementing <spec title>` (empty commit with `--allow-empty`)

### B. Plan

- Add an implementation plan as a `## Implementation Plan` section at the bottom of the spec
- Commit: `docs: add implementation plan for <spec title>`

### C. Implement

For each major step in the plan:

1. Implement the step
2. Run `make fmt` to format
3. Commit: `feat: <short description of what was done>`

Keep commits granular — one per logical change (new model, new handler, new tests, frontend changes, etc.)

### D. QA

1. Run `make build-backend build-client lint-back test`
2. If anything fails, fix it and commit the fix
3. Repeat until all checks pass
4. Commit: `chore: all checks passing for <spec title>`

### E. Archive

1. Determine the target path: `specs/done/YYYY/MM/<spec-filename>` (using the spec's date prefix)
2. Create the directory if needed: `mkdir -p specs/done/YYYY/MM/`
3. `git mv specs/todos/<spec-file> specs/done/YYYY/MM/<spec-file>`
4. Commit: `docs: archive completed spec <spec title>`

### F. Merge

1. Switch back to `main`
2. Merge the feature branch: `git merge --no-ff <branch>`
3. Move to the next spec

## Key Rules

- **Do not wait for user approval**
- **Never skip QA** — all checks must pass before archiving
- **Commit frequently** — at minimum: start, plan, each major step, QA pass, archive
- If `specs/todos/` is empty, tell the user there's nothing to implement and suggest they move specs there
- Process specs in chronological order (oldest date first)