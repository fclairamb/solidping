# Docker Multistage Build

## Requirements
Create a Dockerfile that builds the SolidPing application using a multistage build approach.

## Build Stages

### Stage 1: Frontend Build
- Use a Node.js image (with pnpm support)
- Copy frontend source from `front/` directory
- Install dependencies with `pnpm install`
- Build frontend with `pnpm run build`
- Output: `front/dist/` containing built frontend assets

### Stage 2: Backend Build
- Use a Go 1.24+ image
- Copy backend source from `back/` directory
- Copy frontend build artifacts from Stage 1 to `back/internal/app/res/`
- Run `go build` to compile the backend
- The Go binary will embed the frontend via `go:embed` directive
- Output: Single `solidping` binary

### Stage 3: Runtime
- Use distroless debian13 as the minimal base image
- Copy only the compiled `solidping` binary from Stage 2
- Set appropriate user permissions (non-root)
- Expose port 4000 (default listen port)
- Set entrypoint to run `solidping serve`

## Build Context
- Build from repository root
- Dockerfile should be at repository root

## Environment Variables
The application supports configuration via environment variables:
- `DB_TYPE`: Database type (sqlite or postgres)
- `DB_URL`: PostgreSQL connection string (if using postgres)
- `DB_DIR`: SQLite data directory (if using sqlite)
- `SERVER_LISTEN`: Listen address (default: `:4000`)

## Volume Mounts
- SQLite mode: Mount volume at `/data` for database persistence
- PostgreSQL mode: No volume needed (external database)

## Example Usage
```bash
# Build
docker build -t solidping:latest .

# Run with SQLite
docker run -p 4000:4000 -v solidping-data:/data \
  -e DB_TYPE=sqlite -e DB_DIR=/data \
  solidping:latest

# Run with PostgreSQL
docker run -p 4000:4000 \
  -e DB_TYPE=postgres \
  -e DB_URL="postgresql://user:pass@host:5432/db" \
  solidping:latest
```