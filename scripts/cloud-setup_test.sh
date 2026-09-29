#!/usr/bin/env bash
# Self-test for scripts/cloud-setup.sh: idempotent second run and the two
# failure paths (Go missing, network blocked). Needs go, bun and golangci-lint
# already installed (it never touches the network on the happy path).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

# 1. Go missing: non-zero exit, message with install instructions.
mkdir "$tmp/empty"
if out="$(PATH="$tmp/empty:/usr/bin:/bin" HOME="$tmp/home1" "$(command -v bash)" "$here/cloud-setup.sh" 2>&1)"; then
    fail "missing Go should fail"
fi
[[ "$out" == *"https://go.dev/dl/"* ]] || fail "missing Go should say where to get it: $out"

# 2. Network blocked, bun absent: non-zero exit naming the unreachable host.
bin="$tmp/bin"
mkdir "$bin"
ln -s "$(command -v go)" "$bin/go"
for tool in awk sort head uname mktemp curl unzip dirname cat sed tar install rm mkdir grep; do
    ln -s "$(command -v "$tool")" "$bin/$tool"
done
if out="$(PATH="$bin" HOME="$tmp/home2" https_proxy=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 \
    "$(command -v bash)" "$here/cloud-setup.sh" 2>&1)"; then
    fail "blocked network should fail"
fi
[[ "$out" == *"github.com"* ]] || fail "blocked network should name github.com: $out"

# 3. Idempotent: two runs in a row exit 0 and the second reports skips only.
"$here/cloud-setup.sh" >/dev/null 2>&1 || fail "first run should pass"
out="$("$here/cloud-setup.sh" 2>&1)" || fail "second run should pass"
[[ "$out" == *"dash0 already embedded"* ]] || fail "second run should skip the frontend build: $out"
[[ "$out" != *"installed to"* ]] || fail "second run should install nothing: $out"
echo "cloud-setup self-test passed"
