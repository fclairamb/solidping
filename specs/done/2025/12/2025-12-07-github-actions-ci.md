# GitHub Actions CI

**Type**: chore
**Branch**: `chore/github-actions-ci`

## Summary

Add GitHub Actions CI workflow to build and lint both frontend and backend on every push and pull request.

## Requirements

### Backend (Go 1.25)
- Run golangci-lint with the existing `.golangci.yml` configuration
- Run `go test ./...` to execute all tests
- Build the Go binary to verify compilation

### Frontend (Bun)
- Install dependencies with Bun
- Run ESLint (`bun run lint`)
- Build the frontend (`bun run build`)

### Triggers
- On push to `main` branch
- On pull requests to `main` branch

## Acceptance Criteria

- [ ] CI workflow triggers on push to main and PRs
- [ ] Backend: golangci-lint runs with existing config
- [ ] Backend: Tests run successfully
- [ ] Backend: Binary builds successfully
- [ ] Frontend: ESLint passes
- [ ] Frontend: TypeScript compilation and Vite build succeed
- [ ] Final binary includes embedded frontend assets
- [ ] Jobs run in parallel where possible for faster feedback
- [ ] Latest stable versions of all tools are used

## Technical Considerations

### Tool Versions (Latest Stable)
- Go: 1.25
- golangci-lint: v2 (uses `version: v2` in action)
- Bun: latest

### Workflow Structure
- Three jobs:
  1. `backend-lint`: Run golangci-lint and tests (parallel with frontend)
  2. `frontend`: Build frontend with Bun (parallel with backend-lint)
  3. `build`: Build final binary with embedded frontend (depends on frontend job)
- Use official GitHub Actions where possible
- Use `golangci/golangci-lint-action` for linting
- Use artifacts to pass frontend dist between jobs
- Cache dependencies for faster runs

### Build Process
1. Frontend job builds and uploads `dist/` as artifact
2. Build job downloads frontend artifact
3. Copy frontend dist to `back/internal/app/res/`
4. Build Go binary with embedded frontend (`make build-backend` or `go build`)

## Implementation Plan

### File to Create
- `.github/workflows/ci.yml`

### Workflow Jobs

#### Job 1: `backend-lint`
```yaml
- uses: actions/checkout@v4
- uses: actions/setup-go@v5 with go-version: '1.25'
- uses: golangci/golangci-lint-action@v7 with working-directory: back
- run: go test ./... (in back/)
```

#### Job 2: `frontend`
```yaml
- uses: actions/checkout@v4
- uses: oven-sh/setup-bun@v2
- run: bun install (in apps/dashboard/)
- run: bun run lint
- run: bun run build
- uses: actions/upload-artifact@v4 with path: apps/dashboard/dist
```

#### Job 3: `build` (needs: frontend)
```yaml
- uses: actions/checkout@v4
- uses: actions/setup-go@v5 with go-version: '1.25'
- uses: actions/download-artifact@v4 to back/internal/app/res/
- run: go build (in back/)
```

## Implementation Notes

(To be filled during implementation)
