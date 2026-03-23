# Switch from @vitejs/plugin-react-swc to @vitejs/plugin-react

## Context
Vite now recommends switching to `@vitejs/plugin-react` when no SWC plugins are used, as it offers improved performance with the new Rolldown bundler.

Warning message:
```
[vite:react-swc] We recommend switching to `@vitejs/plugin-react` for improved performance as no swc plugins are used.
```

## Changes

1. In `web/dash0/package.json`:
   - Remove `@vitejs/plugin-react-swc`
   - Add `@vitejs/plugin-react`

2. In `web/dash0/vite.config.ts`:
   - Replace `import react from '@vitejs/plugin-react-swc'` with `import react from '@vitejs/plugin-react'`

3. Run `bun install` to update dependencies

## Implementation Plan

1. Remove `@vitejs/plugin-react-swc` and add `@vitejs/plugin-react` in package.json
2. Update import in vite.config.ts
3. Run `bun install` to update lockfile
4. Build and lint to verify

## Validation
- `make dev-dash0` starts without the warning
- `make build-dash0` succeeds
- Dashboard works correctly
