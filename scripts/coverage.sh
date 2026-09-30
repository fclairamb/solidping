#!/usr/bin/env bash
# Backend coverage helper (spec 2026-09-29-06).
#
#   scripts/coverage.sh filter <in.out> <out.out>   drop generated/vendored code
#   scripts/coverage.sh total  <profile.out>         print the total percentage (number only)
#   scripts/coverage.sh gate   <profile.out>         compare the total against $COVERAGE_MIN
#   scripts/coverage.sh badge  <profile.out>         print the shields.io endpoint JSON (spec 2026-09-29-10)
#
# The profile comes from `go test -coverprofile -coverpkg=./...`, so every
# package's test binary writes a line for every block of every package. Those
# duplicate lines are merged (a block counts as covered when ANY binary hit it)
# by `go tool cover`, which is why totals are always read through it.

set -euo pipefail

# Paths (relative to the server/ module) excluded from the measurement:
#   pkg/client      oapi-codegen output
#   third_party     vendored forks (grdp)
#   *_generated.go / mock_*.go / *_mock.go  other generated or mock code
EXCLUDE_RE='/server/(pkg/client|third_party)/|_generated\.go:|/mock_[^/]*\.go:|_mock\.go:'

usage() {
  echo "usage: $0 filter <in> <out> | total <profile> | gate <profile> | badge <profile>" >&2
  exit 2
}

cmd="${1:-}"
case "$cmd" in
  filter)
    [[ $# -eq 3 ]] || usage
    grep -vE "$EXCLUDE_RE" "$2" > "$3"
    ;;
  total)
    [[ $# -eq 2 ]] || usage
    go tool cover -func="$2" | awk '/^total:/ {gsub("%","",$3); print $3}'
    ;;
  gate)
    [[ $# -eq 2 ]] || usage
    total="$(go tool cover -func="$2" | awk '/^total:/ {gsub("%","",$3); print $3}')"
    min="${COVERAGE_MIN:-80}"
    line="Backend coverage: ${total}% (minimum ${min}%)"
    echo "$line"
    if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
      echo "### $line" >> "$GITHUB_STEP_SUMMARY"
    fi
    if awk -v t="$total" -v m="$min" 'BEGIN { exit !(t+0 < m+0) }'; then
      echo "::error::Backend coverage ${total}% is below the minimum ${min}% (COVERAGE_MIN)"
      exit 1
    fi
    ;;
  badge)
    [[ $# -eq 2 ]] || usage
    [[ -s "$2" ]] || { echo "coverage profile missing or empty: $2" >&2; exit 1; }
    total="$(go tool cover -func="$2" | awk '/^total:/ {gsub("%","",$3); print $3}')"
    [[ "$total" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "cannot read a coverage total from $2" >&2; exit 1; }
    min="${COVERAGE_MIN:-80}"
    color="$(awk -v t="$total" -v m="$min" 'BEGIN { if (t+0 >= m+0) print "brightgreen"; else if (t+0 >= m-5) print "yellow"; else print "red" }')"
    printf '{"schemaVersion":1,"label":"coverage","message":"%s%%","color":"%s"}\n' "$total" "$color"
    ;;
  *)
    usage
    ;;
esac
