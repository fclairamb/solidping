# The `sp` CLI, on its own.
#
# It is deliberately NOT a stage of the main Dockerfile: that image embeds
# dash0, status0 and the docs site and takes minutes to build, while everything
# `sp` needs is the Go module. A CI job that only wants to run
# `sp checks validate config.yaml` against a committed manifest should not pull
# a ~200 MB server image to do it (spec 2026-09-11-04).
#
# Published as ghcr.io/<repo>/sp on every tag, alongside the four release
# archives. The offline validator needs no token and no network:
#
#   docker run --rm -v "$PWD:/w" -w /w ghcr.io/fclairamb/solidping/sp \
#     checks validate config.yaml
FROM golang:1.27.1-trixie AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG GIT_TIME=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

COPY server/go.mod server/go.sum ./server/

WORKDIR /build/server

RUN go mod download

COPY server/ ./

# CGO off: the CLI talks HTTP and reads files, and a static binary is what
# makes the distroless stage below (and the raw release archives) work on every
# glibc.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w \
      -X 'github.com/fclairamb/solidping/server/internal/version.Version=${VERSION}' \
      -X 'github.com/fclairamb/solidping/server/internal/version.Commit=${COMMIT}' \
      -X 'github.com/fclairamb/solidping/server/internal/version.GitTime=${GIT_TIME}'" \
    -o /sp ./cmd/sp

FROM gcr.io/distroless/static-debian13:nonroot

WORKDIR /w

COPY --from=builder /sp /usr/local/bin/sp

ENTRYPOINT ["/usr/local/bin/sp"]
