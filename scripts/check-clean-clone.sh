#!/usr/bin/env bash
# Regression guard for "the repo compiles and tests from a fresh clone": clone
# the committed HEAD, then build and run the SQLite test layer with no frontend
# build, no Docker and no .env. Meant for nightly CI or a cloud sandbox.
set -euo pipefail

usage() {
    cat <<USAGE
Usage: ${0##*/} [-h]

Clones the current HEAD into a temp dir, runs 'go build ./...' and 'make test'.
Set CLEAN_CLONE_TEST_TARGET to a make target other than 'test' (for example a
narrower one) to shorten the run.
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
readonly TARGET="${CLEAN_CLONE_TEST_TARGET:-test}"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

git clone --quiet --local "${ROOT}" "${tmp}/repo"
cd "${tmp}/repo"

echo "clean clone at $(git rev-parse --short HEAD): go build ./..."
(cd server && go build ./...)
echo "clean clone: make ${TARGET}"
make "${TARGET}"
echo "clean-clone check passed"
