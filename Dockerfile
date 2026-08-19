# Stage 1a: Dash Build
FROM node:24-alpine AS dash-builder

# Install bun
RUN apk add --no-cache curl unzip bash && \
    curl -fsSL https://bun.sh/install | bash && \
    ln -s /root/.bun/bin/bun /usr/local/bin/bun

WORKDIR /build/dash

# Copy dash package files
COPY web/dash/package.json web/dash/bun.lock ./

# Install dependencies
RUN bun install --frozen-lockfile

# Copy dash source
COPY web/dash/ ./

# Build dash
RUN bun run build

# Stage 1b: Dash0 Build
FROM node:24-alpine AS dash0-builder

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

# Stage 1c: Status0 Build
FROM node:24-alpine AS status0-builder

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

# Stage 1d: Docs Build (Docusaurus, incl. generated API reference)
FROM node:24-alpine AS docs-builder

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

# Copy docs source
COPY web/docs/ ./

# Build docs (runs gen-api-docs then docusaurus build)
RUN bun run build

# Stage 2: Backend Build
FROM golang:1.27.0-trixie AS backend-builder

# Build arguments for version information
ARG VERSION=dev
ARG COMMIT=unknown
ARG GIT_TIME=unknown

# Install build dependencies for CGO (needed for SQLite)
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Copy go module files
COPY server/go.mod server/go.sum ./server/

# Download dependencies
WORKDIR /build/server
RUN go mod download

# Copy backend source
COPY server/ ./

# Copy dash build artifacts to embed location
COPY --from=dash-builder /build/dash/dist ./internal/app/res
COPY --from=dash0-builder /build/dash0/dist ./internal/app/dash0res
COPY --from=status0-builder /build/status0/dist ./internal/app/status0res
COPY --from=docs-builder /build/web/docs/build ./internal/app/docsres

# Build the backend binary with version information
# CGO is needed for SQLite support
RUN CGO_ENABLED=1 go build \
    -ldflags "\
      -X 'github.com/fclairamb/solidping/server/internal/version.Version=${VERSION}' \
      -X 'github.com/fclairamb/solidping/server/internal/version.Commit=${COMMIT}' \
      -X 'github.com/fclairamb/solidping/server/internal/version.GitTime=${GIT_TIME}'" \
    -o /solidping .

# Stage 3: Final Runtime Image
FROM gcr.io/distroless/base-debian13:nonroot

WORKDIR /app

# Copy the compiled binary
COPY --from=backend-builder /solidping /app/solidping

# Expose default port
EXPOSE 4000

# Set entrypoint
ENTRYPOINT ["/app/solidping", "serve"]
