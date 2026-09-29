#!/usr/bin/env bash
# Self-test for scripts/coverage.sh: generated code is filtered out, and the
# gate passes below / fails above the measured total. Needs `go` (go tool cover).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/raw.out" <<'P'
mode: set
github.com/fclairamb/solidping/server/internal/version/version.go:1.1,2.2 4 1
github.com/fclairamb/solidping/server/internal/version/version.go:3.1,4.2 4 0
github.com/fclairamb/solidping/server/pkg/client/client_generated.go:1.1,2.2 100 0
github.com/fclairamb/solidping/server/third_party/grdp/x.go:1.1,2.2 100 0
github.com/fclairamb/solidping/server/internal/b/mock_thing.go:1.1,2.2 100 0
P

"$here/coverage.sh" filter "$tmp/raw.out" "$tmp/f.out"
if grep -qE 'pkg/client|third_party|mock_' "$tmp/f.out"; then
  echo "FAIL: generated paths survived the filter" >&2; exit 1
fi
# A real (tiny) profile for the total and the gate.
(cd "$here/../server" && go test -count=1 -coverprofile="$tmp/real.out" ./internal/version >/dev/null)
total="$(cd "$here/../server" && "$here/coverage.sh" total "$tmp/real.out")"
[[ "$total" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "FAIL: total not a number: $total" >&2; exit 1; }

(cd "$here/../server" && COVERAGE_MIN=0 "$here/coverage.sh" gate "$tmp/real.out" >/dev/null) || { echo "FAIL: gate should pass" >&2; exit 1; }
if out="$(cd "$here/../server" && COVERAGE_MIN=101 "$here/coverage.sh" gate "$tmp/real.out" 2>&1)"; then
  echo "FAIL: gate should fail at 101" >&2; exit 1
fi
grep -q "${total}%" <<<"$out" && grep -q '101%' <<<"$out" || { echo "FAIL: gate output lacks total/threshold" >&2; exit 1; }
echo "coverage.sh self-test ok"
