# Stage 1a: Dash0 Build
#
# Pinned to --platform=$BUILDPLATFORM: this stage only produces static
# assets (no native code), so it must run natively on the build host rather
# than under QEMU emulation for the target platform (see backend-builder
# below for the stage that actually varies per target).
FROM --platform=$BUILDPLATFORM node:24-alpine AS dash0-builder

# Install bun
RUN apk add --no-cache curl unzip bash && \
    curl -fsSL https://bun.sh/install | bash && \
    ln -s /root/.bun/bin/bun /usr/local/bin/bun

WORKDIR /build/dash0

# Copy dash0 package files
COPY web/dash0/package.json web/dash0/bun.lock ./

# Install dependencies
RUN bun install --frozen-lockfile

# Copy dash0 source
COPY web/dash0/ ./

# Build dash0
RUN bun run build

# Stage 1b: Status0 Build (static assets — see dash0-builder above)
FROM --platform=$BUILDPLATFORM node:24-alpine AS status0-builder

# Install bun
RUN apk add --no-cache curl unzip bash && \
    curl -fsSL https://bun.sh/install | bash && \
    ln -s /root/.bun/bin/bun /usr/local/bin/bun

WORKDIR /build/status0

# Copy status0 package files
COPY web/status0/package.json web/status0/bun.lock ./

# Install dependencies
RUN bun install --frozen-lockfile

# Copy status0 source
COPY web/status0/ ./

# Build status0
RUN bun run build

# Stage 1c: Docs Build (Docusaurus, incl. generated API reference — static assets, see dash0-builder above)
FROM --platform=$BUILDPLATFORM node:24-alpine AS docs-builder

# Install bun
RUN apk add --no-cache curl unzip bash && \
    curl -fsSL https://bun.sh/install | bash && \
    ln -s /root/.bun/bin/bun /usr/local/bin/bun

WORKDIR /build/web/docs

# Copy docs package files
COPY web/docs/package.json web/docs/bun.lock ./

# Install dependencies
RUN bun install --frozen-lockfile

# The API reference is generated at build time from the canonical OpenAPI spec
# via the relative path ../../server/internal/app/openapi/openapi.yaml — make it
# available at that location in this stage.
COPY server/internal/app/openapi/openapi.yaml /build/server/internal/app/openapi/openapi.yaml

# The changelog page is generated at build time from the root CHANGELOG.md via
# the relative path ../../../CHANGELOG.md (see scripts/gen-changelog.ts) — make
# it available at that location in this stage.
COPY CHANGELOG.md /build/CHANGELOG.md

# Copy docs source
COPY web/docs/ ./

# Build docs (runs gen-api-docs then docusaurus build)
RUN bun run build

# Stage 2: Backend Build
#
# Pinned to --platform=$BUILDPLATFORM: the Go toolchain itself always runs
# natively on the build host, and cross-compiles the OUTPUT binary for
# TARGETOS/TARGETARCH via the CGO_ENABLED=0 build below (no QEMU emulation
# needed for the compiler, unlike a CGO build would require).
#
# The dependency layer is its own stage so CI can build it on every PR
# (`docker build --target backend-deps .`); the image itself is only built on
# tags. Every local `replace` target in server/go.mod needs its go.mod/go.sum
# copied here, or `go mod download` fails before the source is copied in.
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie AS backend-deps

WORKDIR /build

COPY server/go.mod server/go.sum ./server/
COPY server/third_party/grdp/go.mod server/third_party/grdp/go.sum ./server/third_party/grdp/

WORKDIR /build/server
RUN go mod download

FROM backend-deps AS backend-builder

# Build arguments for version information
ARG VERSION=dev
ARG COMMIT=unknown
ARG GIT_TIME=unknown

# Set by buildx to the platform requested via `--platform` on the final
# image (e.g. "linux" / "arm64"), independent of the build host.
ARG TARGETOS
ARG TARGETARCH

# Copy backend source
COPY server/ ./

# Copy SPA build artifacts to embed locations
COPY --from=dash0-builder /build/dash0/dist ./internal/app/dash0res
COPY --from=status0-builder /build/status0/dist ./internal/app/status0res
COPY --from=docs-builder /build/web/docs/build ./internal/app/docsres

# Build the backend binary with version information. CGO is NOT needed: the
# shipped SQLite driver is pure-Go modernc
# (internal/db/sqlitedriver/sqlitedriver.go), so this cross-compiles cleanly
# for GOOS/GOARCH without a C toolchain or QEMU emulation — the same way the
# release binaries (ci.yml) and the sp CLI image already build.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags "\
      -X 'github.com/fclairamb/solidping/server/internal/version.Version=${VERSION}' \
      -X 'github.com/fclairamb/solidping/server/internal/version.Commit=${COMMIT}' \
      -X 'github.com/fclairamb/solidping/server/internal/version.GitTime=${GIT_TIME}'" \
    -o /solidping .

# The final stage is distroless and has no shell, so /data/files can't be
# created there. Create it here and copy it across with the right ownership
# (see the final stage) so a fresh named volume seeds correctly.
RUN mkdir -p /data/files

# Stage 3: Final Runtime Image
FROM gcr.io/distroless/base-debian13:nonroot

WORKDIR /app

# Copy the compiled binary
COPY --from=backend-builder /solidping /app/solidping

# Seed /data (database + uploads) owned by the nonroot user (65532:65532) so
# a fresh named volume, which Docker populates from the image directory's
# content and ownership, is writable on first run.
COPY --from=backend-builder --chown=65532:65532 /data /data

# The image is self-contained by default: SQLite database and uploads live
# under /data, so `docker run -v solidping-data:/data ...` is enough. Every
# value can still be overridden with -e (SP_DB_TYPE=postgres + SP_DB_URL for
# Postgres, for example).
ENV SP_DB_TYPE=sqlite \
    SP_DB_DIR=/data \
    SP_FILESTORAGE_LOCAL_ROOT=/data/files
VOLUME /data

# Expose default port
EXPOSE 4000

# The image has no shell/curl (distroless), so the probe is the binary
# itself calling its own /api/mgmt/health over loopback
# (internal/healthcheck). 503 during the graceful-shutdown window makes the
# container report unhealthy before it stops, which is the wanted signal.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/app/solidping", "healthcheck"]

# Set entrypoint
ENTRYPOINT ["/app/solidping", "serve"]
