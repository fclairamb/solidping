#!/usr/bin/env bash
# Lints the agent instruction files (AGENTS.md at any level).
#   > 500 lines: error   > 300 lines: warning   > 20480 bytes: error
#   a CLAUDE.md beside an AGENTS.md must be exactly the `@AGENTS.md` stub
#   a CLAUDE.md with no AGENTS.md beside it is an error
# Spec 2026-09-29-07.

set -euo pipefail

readonly MAX_LINES=500
readonly WARN_LINES=300
readonly MAX_BYTES=20480

usage() {
    echo "usage: $0 [root-dir]" >&2
    echo "  Checks every AGENTS.md / CLAUDE.md under root-dir (default: repo root)." >&2
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi
if [[ $# -gt 1 ]]; then
    usage
    exit 2
fi

root="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
errors=0

find_docs() {
    find "$root" \
        \( -name node_modules -o -name .git -o -name .bb -o -path "$root/.claude/worktrees" \) -prune -o \
        -type f -name "$1" -print
}

while IFS= read -r file; do
    lines="$(wc -l < "$file" | tr -d ' ')"
    bytes="$(wc -c < "$file" | tr -d ' ')"
    rel="${file#"$root"/}"
    if ((lines > MAX_LINES)); then
        echo "error: $rel has $lines lines (hard limit $MAX_LINES)" >&2
        errors=$((errors + 1))
    elif ((lines > WARN_LINES)); then
        echo "warning: $rel has $lines lines (target under $WARN_LINES)" >&2
    fi
    if ((bytes > MAX_BYTES)); then
        echo "error: $rel is $bytes bytes (limit $MAX_BYTES)" >&2
        errors=$((errors + 1))
    fi
    stub="$(dirname "$file")/CLAUDE.md"
    if [[ -f "$stub" && "$(tr -d '[:space:]' < "$stub")" != "@AGENTS.md" ]]; then
        echo "error: ${stub#"$root"/} must contain only the @AGENTS.md stub" >&2
        errors=$((errors + 1))
    fi
done < <(find_docs AGENTS.md)

while IFS= read -r file; do
    if [[ ! -f "$(dirname "$file")/AGENTS.md" ]]; then
        echo "error: ${file#"$root"/} has no AGENTS.md beside it" >&2
        errors=$((errors + 1))
    fi
done < <(find_docs CLAUDE.md)

if ((errors > 0)); then
    echo "agent docs check failed: $errors error(s)" >&2
    exit 1
fi
echo "agent docs check passed"
