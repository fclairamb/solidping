#!/usr/bin/env bash
# Create placeholder files in the gitignored embedded-frontend directories so
# `go test ./...` works on a clone with no frontend build. Only creates files
# that are missing: a real build (make copy-dash0 ...) is never overwritten.
# The placeholder set mirrors the "Create embed placeholders" step in
# .github/workflows/ci.yml.
set -euo pipefail

usage() {
    cat <<USAGE
Usage: ${0##*/} [-h]

Create missing placeholder files under server/internal/app/{dash0,status0,docs}res.
Existing files are left untouched, so a real frontend build is preserved.
USAGE
}

case "${1:-}" in
    -h | --help)
        usage
        exit 0
        ;;
    "") ;;
    *)
        usage >&2
        exit 2
        ;;
esac

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly APP_DIR="${ROOT}/server/internal/app"

# placeholder <path> <content>: write the file only when it does not exist.
placeholder() {
    local path="$1"
    local content="$2"
    if [[ -e "${path}" ]]; then
        return 0
    fi
    mkdir -p "$(dirname "${path}")"
    printf '%s\n' "${content}" >"${path}"
    echo "created ${path#"${ROOT}"/}"
}

placeholder "${APP_DIR}/dash0res/index.html" "placeholder"

placeholder "${APP_DIR}/status0res/index.html" "placeholder"
placeholder "${APP_DIR}/status0res/embed/v1/widget.js" "(()=>{})();"
# TestCompressionEncodesEmbeddedStatus0JS needs a bundle .js whose gzipped size
# is above net/http's ~2 KiB buffer, so the placeholder is incompressible.
if ! find "${APP_DIR}/status0res/assets" -name '*.js' 2>/dev/null | grep -q .; then
    mkdir -p "${APP_DIR}/status0res/assets"
    head -c 16384 /dev/urandom | base64 >"${APP_DIR}/status0res/assets/placeholder.js"
    echo "created server/internal/app/status0res/assets/placeholder.js"
fi

placeholder "${APP_DIR}/docsres/index.html" "placeholder"
placeholder "${APP_DIR}/docsres/404.html" "placeholder"
placeholder "${APP_DIR}/docsres/llms.txt" "placeholder"
placeholder "${APP_DIR}/docsres/llms-full.txt" "placeholder"
placeholder "${APP_DIR}/docsres/search-index.json" "[]"
