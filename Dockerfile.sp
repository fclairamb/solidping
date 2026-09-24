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
# --platform=$BUILDPLATFORM pins the builder stage to the machine doing the
# building, NOT to each target. Without it a linux/arm64 target runs this whole
# stage — go mod download and the compile — under QEMU emulation on an amd64
# runner, which is both minutes slower and needs a QEMU setup step in CI. The
# stage already cross-compiles properly via TARGETOS/TARGETARCH below, so
# emulating it buys nothing.
#
# The dependency layer is its own stage so CI can build it on every PR
# (`docker build -f Dockerfile.sp --target deps .`); the image itself is only
# built on tags. Every local `replace` target in server/go.mod needs its
# go.mod/go.sum copied here.
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie AS deps

WORKDIR /build

COPY server/go.mod server/go.sum ./server/
COPY server/third_party/grdp/go.mod server/third_party/grdp/go.sum ./server/third_party/grdp/

WORKDIR /build/server

RUN go mod download

FROM deps AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG GIT_TIME=unknown
ARG TARGETOS
ARG TARGETARCH

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
