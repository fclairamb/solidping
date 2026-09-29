#!/usr/bin/env bash
# Bootstrap a fresh Linux sandbox (Claude cloud session, CI-like container) so
# `make lint`, `make test` and `make dev` work with no Docker and no secrets.
# Idempotent: a second run downloads and builds nothing. See
# wiki/runbooks/claude-cloud.md.
set -euo pipefail

# Pinned by download. golangci-lint follows .github/workflows/ci.yml; bun is
# not pinned in CI (oven-sh/setup-bun@v2), this is the version we develop with.
readonly BUN_VERSION="${BUN_VERSION:-1.3.4}"

usage() {
    cat <<USAGE
Usage: ${0##*/} [--with-playwright] [--force-frontend] [-h]

Verifies Go (never installs it), installs bun and golangci-lint into
~/.local/bin by pinned download when missing, runs 'make deps', then builds and
embeds dash0 and status0 so 'go build ./...' serves a real UI.

Options:
  --with-playwright  also install the Chromium browser for Playwright E2E
  --force-frontend   rebuild dash0 and status0 even when already embedded
  -h, --help         show this help

Network hosts needed: proxy.golang.org, sum.golang.org, registry.npmjs.org,
github.com (bun, golangci-lint), and with --with-playwright cdn.playwright.dev.
USAGE
}

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly BIN_DIR="${HOME}/.local/bin"
WITH_PLAYWRIGHT=false
FORCE_FRONTEND=false
SKIPPED=()
DONE=()

while (($# > 0)); do
    case "$1" in
        --with-playwright) WITH_PLAYWRIGHT=true ;;
        --force-frontend) FORCE_FRONTEND=true ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage >&2
            exit 2
            ;;
    esac
    shift
done

die() {
    echo "error: $*" >&2
    exit 1
}

readonly PATH_ORIG="${PATH}"
export PATH="${BIN_DIR}:${PATH}"
mkdir -p "${BIN_DIR}"

# download <url> <dest>: fetch a file, naming the host when it is unreachable.
download() {
    local url="$1"
    local dest="$2"
    local host="${url#*://}"
    host="${host%%/*}"
    if ! curl -fsSL --retry 2 --connect-timeout 15 -o "${dest}" "${url}"; then
        die "cannot download ${url}: host ${host} is unreachable or blocked (add it to the sandbox network allowlist)"
    fi
}

platform_os() {
    case "$(uname -s)" in
        Linux) echo linux ;;
        Darwin) echo darwin ;;
        *) die "unsupported OS $(uname -s)" ;;
    esac
}

platform_arch() {
    case "$(uname -m)" in
        x86_64 | amd64) echo x64 ;;
        aarch64 | arm64) echo aarch64 ;;
        *) die "unsupported CPU $(uname -m)" ;;
    esac
}

verify_go() {
    local want have
    want="$(awk '/^go [0-9]/ {print $2; exit}' "${ROOT}/server/go.mod")"
    if ! command -v go >/dev/null 2>&1; then
        die "Go ${want} or newer is required but 'go' is not on PATH. Install it from https://go.dev/dl/ (this script never installs Go), then re-run."
    fi
    have="$(go env GOVERSION)"
    have="${have#go}"
    if [[ "$(printf '%s\n%s\n' "${want}" "${have}" | sort -V | head -n1)" != "${want}" ]]; then
        die "Go ${want} or newer is required, found ${have}. Install a newer Go from https://go.dev/dl/ and re-run."
    fi
    DONE+=("go ${have} verified (need >= ${want})")
}

install_bun() {
    if command -v bun >/dev/null 2>&1; then
        SKIPPED+=("bun already installed ($(bun --version))")
        return 0
    fi
    command -v unzip >/dev/null 2>&1 || die "'unzip' is required to install bun; install it and re-run"
    local tmp asset
    tmp="$(mktemp -d)"
    asset="bun-$(platform_os)-$(platform_arch)"
    download "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/${asset}.zip" "${tmp}/bun.zip"
    unzip -q "${tmp}/bun.zip" -d "${tmp}"
    install -m 0755 "${tmp}/${asset}/bun" "${BIN_DIR}/bun"
    rm -rf "${tmp}"
    DONE+=("bun ${BUN_VERSION} installed to ${BIN_DIR}")
}

install_golangci_lint() {
    local want have arch tmp asset
    want="$(sed -n 's/^ *version: v\(2\.[0-9.]*\)$/\1/p' "${ROOT}/.github/workflows/ci.yml" | head -n1)"
    [[ -n "${want}" ]] || die "cannot read the golangci-lint version from .github/workflows/ci.yml"
    if command -v golangci-lint >/dev/null 2>&1; then
        have="$(golangci-lint version --short 2>/dev/null || true)"
        have="${have#v}"
        if [[ "${have}" == "${want}" ]]; then
            SKIPPED+=("golangci-lint ${want} already installed")
            return 0
        fi
    fi
    case "$(uname -m)" in
        x86_64 | amd64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        *) die "unsupported CPU $(uname -m)" ;;
    esac
    tmp="$(mktemp -d)"
    asset="golangci-lint-${want}-$(platform_os)-${arch}"
    download "https://github.com/golangci/golangci-lint/releases/download/v${want}/${asset}.tar.gz" "${tmp}/gl.tgz"
    tar -xzf "${tmp}/gl.tgz" -C "${tmp}"
    install -m 0755 "${tmp}/${asset}/golangci-lint" "${BIN_DIR}/golangci-lint"
    rm -rf "${tmp}"
    DONE+=("golangci-lint ${want} installed to ${BIN_DIR}")
}

# embedded <dir>: true when the directory holds a real build, not a placeholder.
embedded() {
    local index="${ROOT}/server/internal/app/$1/index.html"
    [[ -f "${index}" ]] && ! grep -qx placeholder "${index}"
}

build_frontend() {
    local name
    for name in dash0 status0; do
        if ! ${FORCE_FRONTEND} && embedded "${name}res"; then
            SKIPPED+=("${name} already embedded (use --force-frontend to rebuild)")
            continue
        fi
        make -C "${ROOT}" "build-${name}" "copy-${name}"
        DONE+=("${name} built and embedded")
    done
    "${ROOT}/scripts/embed-placeholders.sh" >/dev/null
    SKIPPED+=("docs site not built (placeholders only; run 'make build-docs copy-docs' if needed)")
}

install_playwright() {
    if ! ${WITH_PLAYWRIGHT}; then
        SKIPPED+=("Playwright browsers (pass --with-playwright)")
        return 0
    fi
    (cd "${ROOT}/web/dash0" && bunx playwright install chromium) ||
        die "playwright install failed: check that cdn.playwright.dev is reachable"
    DONE+=("Playwright Chromium installed")
}

verify_go
install_bun
install_golangci_lint
make -C "${ROOT}" deps
DONE+=("make deps (go modules, dash0 and status0 node_modules)")
build_frontend
install_playwright

echo
echo "cloud-setup done:"
printf '  + %s\n' "${DONE[@]}"
if ((${#SKIPPED[@]} > 0)); then
    echo "skipped:"
    printf '  - %s\n' "${SKIPPED[@]}"
fi
case ":${PATH_ORIG}:" in
    *":${BIN_DIR}:"*) ;;
    *) echo "note: add ${BIN_DIR} to PATH in new shells" ;;
esac
