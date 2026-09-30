---
model: sonnet
effort: medium
---

# status0 CI reports 10 `react-refresh/only-export-components` warnings that do not fail the build

## Problem
CI run https://github.com/fclairamb/solidping/actions/runs/36275333612 ("Status0 Build" job) shows 10 annotations, all from `react-refresh/only-export-components`:

- `web/status0/src/routes/index.tsx` L12, L24
- `web/status0/src/routes/__root.tsx` L12
- `web/status0/src/routes/$org.tv.tsx` L17
- `web/status0/src/routes/$org.tsx` L7
- `web/status0/src/routes/$org.index.tsx` L12
- `web/status0/src/routes/$org.$slug_.tv.tsx` L18
- `web/status0/src/routes/$org.$slug.tsx` L13
- `web/status0/src/main.tsx` L105 ("file has exports": the file has none)
- `web/status0/src/components/ui/badge.tsx` L49 ("share constants or functions")

Warnings are green in CI, so they pile up unnoticed. They must fail the job, then be fixed.

`web/status0/package.json:11` already has `"lint": "eslint . --max-warnings 0"`, and `web/status0/eslint.config.js` sets the rule to `"warn"`. So either the CI job does not run `bun run lint`, or it runs eslint another way (e.g. a reporter that only annotates). Current line numbers also differ from the annotations (`badge.tsx` no longer exports `badgeVariants`, `main.tsx` is shorter than L105), so the run may predate recent changes.

## Proposal
1. Find how the `status0` job in `.github/workflows/ci.yml` lints (the aggregate job at `ci.yml:887` needs it). Make it run `bun run lint` (`--max-warnings 0`) so any warning fails the job. If it already does and the warnings still pass, find why and fix that.
2. Set `react-refresh/only-export-components` to `"error"` in `web/status0/eslint.config.js` (keeps `allowConstantExport: true`), so a warning cannot go green.
3. Re-run `bun run lint` in `web/status0` on the current tree and fix every finding in code, never by disabling the rule or adding `eslint-disable`:
   - Route files (`web/status0/src/routes/*.tsx`): they export `Route` next to an inline component. Move each component into its own file (e.g. `web/status0/src/components/...` or `routes/-components/`, which TanStack Router ignores) and keep only `export const Route = createFileRoute(...)` in the route file. Check how `web/dash0` handles the same case and copy its convention.
   - `web/status0/src/main.tsx`: move whatever the rule flags out of the file, or restructure so it no longer trips the rule.
   - `web/status0/src/components/ui/badge.tsx`: confirm whether it still warns. If so, move shared non-component exports to a sibling file.
4. Confirm `bun run build` and `bun run typecheck` (whatever `package.json` defines) still pass.

## Tests
- `bun run lint` in `web/status0` exits 0 with zero warnings on the fixed tree.
- Negative: temporarily add `export const x = 1` next to a component in a scratch `.tsx` file; `bun run lint` must exit non-zero (rule is now an error). Remove it after checking.
- `web/status0` Playwright e2e (`web/status0/e2e`), run against a fresh build: status page and TV routes still render after the component moves.

## To verify
- Which CI step in the `status0` job produced the annotations, and why the job stayed green.
- Whether the run predates current HEAD (line numbers do not match): run lint locally first, the real warning list may be shorter than 10.
