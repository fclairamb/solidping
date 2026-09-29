#!/usr/bin/env bash
# Self-test for scripts/check-agent-docs.sh, using fixture trees in a temp dir.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

# fixture <name> <lines> <bytes-per-line-padding>: an AGENTS.md of N lines.
fixture() {
    local dir="$tmp/$1"
    mkdir -p "$dir"
    local pad
    pad="$(printf 'x%.0s' $(seq 1 "$3"))"
    for _ in $(seq 1 "$2"); do echo "$pad"; done > "$dir/AGENTS.md"
    echo "$dir"
}

expect_pass() { "$here/check-agent-docs.sh" "$1" >/dev/null 2>&1 || fail "$2 should pass"; }
expect_fail() { if "$here/check-agent-docs.sh" "$1" >/dev/null 2>&1; then fail "$2 should fail"; fi; }

expect_pass "$(fixture l250 250 10)" "250 lines"

d="$(fixture l350 350 10)"
expect_pass "$d" "350 lines"
out="$("$here/check-agent-docs.sh" "$d" 2>&1)"
[[ "$out" == *"warning:"* ]] || fail "350 lines should warn"

expect_fail "$(fixture l501 501 10)" "501 lines"
expect_fail "$(fixture big 300 70)" "300 lines / 21 KB"

d="$(fixture badstub 10 10)"
echo "# not a stub" > "$d/CLAUDE.md"
expect_fail "$d" "non-stub CLAUDE.md"

d="$(fixture goodstub 10 10)"
echo "@AGENTS.md" > "$d/CLAUDE.md"
expect_pass "$d" "stub CLAUDE.md"

d="$tmp/orphan"
mkdir -p "$d"
echo "@AGENTS.md" > "$d/CLAUDE.md"
expect_fail "$d" "CLAUDE.md without AGENTS.md"

# Ignored trees: node_modules and .claude/worktrees never count.
d="$(fixture ignored 10 10)"
mkdir -p "$d/node_modules/x" "$d/.claude/worktrees/w"
echo "junk" > "$d/node_modules/x/CLAUDE.md"
echo "junk" > "$d/.claude/worktrees/w/CLAUDE.md"
expect_pass "$d" "ignored directories"

echo "check-agent-docs self-test passed"
